package sabnzbd

import (
	"bytes"
	"cmp"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The API's modes, and the actions within them, that Sonarr's SabnzbdProxy
// uses, plus the few around them a test might.
const (
	modeVersion    = "version"
	modeGetConfig  = "get_config"
	modeFullStatus = "fullstatus"
	modeGetCats    = "get_cats"
	modeQueue      = "queue"
	modeHistory    = "history"
	modeAddFile    = "addfile"
	modeAddURL     = "addurl"
	modeRetry      = "retry"
	modePause      = "pause"
	modeResume     = "resume"

	actionDelete = "delete"
)

// maxUpload caps an NZB upload; a real one runs to a few megabytes.
const maxUpload = 64 << 20

// SABnzbd's own words for a failed call (its api.py), which Sonarr puts in
// front of whoever reads its error.
const (
	msgNoValue        = "expects one parameter"
	msgNoItem         = "item does not exist"
	msgNotImplemented = "not implemented"
)

// noScript is how SABnzbd names a job's post-processing script when it has
// none.
const noScript = "None"

// ServeHTTP answers the SABnzbd API on /api (any path ending in /api, so a
// client configured with a URL base such as /sabnzbd works too), always as
// JSON, which is what Sonarr asks for.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api" && !strings.HasSuffix(r.URL.Path, "/api") {
		http.NotFound(w, r)
		return
	}

	up, err := parseRequest(w, r)
	if err != nil {
		writeJSON(w, failure(err.Error()))
		return
	}
	params := r.Form
	mode := params.Get("mode")

	s.mu.Lock()
	s.calls = append(s.calls, Call{
		Method: r.Method,
		Mode:   mode,
		Name:   params.Get("name"),
		Params: cloneValues(params),
		File:   up.filename,
		Time:   time.Now(),
	})
	s.mu.Unlock()

	// SABnzbd answers version to anyone; everything else needs the key, and a
	// missing or wrong one is a JSON error under a 200, whose words Sonarr's
	// TestAuthentication looks for
	if mode != modeVersion && s.apiKey != "" {
		switch params.Get("apikey") {
		case "":
			writeJSON(w, failure("API Key Required"))
			return
		case s.apiKey:
		default:
			writeJSON(w, failure("API Key Incorrect"))
			return
		}
	}

	writeJSON(w, s.answer(mode, params, up))
}

// upload is the NZB file of an addfile.
type upload struct {
	filename string
	data     []byte
}

// parseRequest reads the query and form, and the uploaded file of a
// multipart POST, which is how Sonarr's DownloadNzb sends an NZB: the file in
// a part named "name", cat and priority in the query.
func parseRequest(w http.ResponseWriter, r *http.Request) (upload, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseForm(); err != nil {
			return upload{}, fmt.Errorf("reading the form: %w", err)
		}
		return upload{}, nil
	}
	if err := r.ParseMultipartForm(maxUpload); err != nil { //nolint:gosec // G120: bounded, r.Body is a MaxBytesReader of maxUpload above
		return upload{}, fmt.Errorf("reading the upload: %w", err)
	}
	for _, field := range []string{"name", "nzbfile"} {
		if files := r.MultipartForm.File[field]; len(files) > 0 {
			return readUpload(files[0])
		}
	}

	return upload{}, nil
}

func readUpload(fh *multipart.FileHeader) (upload, error) {
	f, err := fh.Open()
	if err != nil {
		return upload{}, fmt.Errorf("opening %s: %w", fh.Filename, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return upload{}, fmt.Errorf("reading %s: %w", fh.Filename, err)
	}

	return upload{filename: fh.Filename, data: data}, nil
}

// answer is the JSON for one call.
func (s *Server) answer(mode string, params url.Values, up upload) any {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch mode {
	case modeVersion:
		return versionAnswer{Version: s.version}
	case modeGetConfig:
		return s.configAnswer()
	case modeFullStatus:
		return fullStatusAnswer{Status: fullStatus{
			CompleteDir: s.completeDir, DownloadDir: s.incompleteDir(), Version: s.version, Paused: s.paused,
		}}
	case modeGetCats:
		return categoriesAnswer{Categories: []string{"*", s.category}}
	case modeQueue:
		return s.queueCall(params)
	case modeHistory:
		return s.historyCall(params)
	case modeAddFile:
		return s.addFile(params, up)
	case modeAddURL:
		return s.addURL(params)
	case modeRetry:
		return s.retry(params.Get("value"))
	case modePause, modeResume:
		s.paused = mode == modePause
		return statusAnswer{Status: true}
	default:
		return failure(msgNotImplemented)
	}
}

// configAnswer is get_config, as Sonarr's checks of a new client want it: a
// rooted complete folder; the "*" and Sonarr's category, with job folders
// (a dir not ending in *); no sorter active on the category; no pre-check;
// and history kept, so Sonarr does not warn that completed downloads vanish
// before it can import them.
func (s *Server) configAnswer() configAnswer {
	return configAnswer{Config: config{
		Misc: configMisc{
			CompleteDir:            s.completeDir,
			DownloadDir:            s.incompleteDir(),
			PreCheck:               0,
			HistoryRetention:       "",
			HistoryRetentionOption: "all",
			HistoryRetentionNumber: 0,
		},
		Categories: []configCategory{
			{Name: "*", Order: 0, PP: "3", Script: noScript, Dir: "", Priority: PriorityNormal},
			{Name: s.category, Order: 1, PP: "", Script: "Default", Dir: "", Priority: PriorityDefault},
		},
		Servers: []configServer{},
		Sorters: []configSorter{},
	}}
}

// queueCall is mode=queue: the listing, or with name= an action on jobs.
func (s *Server) queueCall(params url.Values) any {
	ids := splitIDs(params.Get("value"))
	switch params.Get("name") {
	case "":
		return s.queueListing(params)
	case actionDelete:
		var removed []string
		for _, j := range s.queue() {
			if ids.match(j.ID) {
				removed = append(removed, j.ID)
				s.drop(j, params.Get("del_files") == "1")
			}
		}
		return addAnswer{Status: true, NzoIDs: emptyIfNil(removed)}
	case modePause, modeResume:
		var changed []string
		for _, j := range s.queue() {
			if ids.match(j.ID) {
				changed = append(changed, j.ID)
				j.Status = StatusPaused
				if params.Get("name") == modeResume {
					j.Status = resumed(j)
				}
			}
		}
		return addAnswer{Status: true, NzoIDs: emptyIfNil(changed)}
	default:
		return failure(msgNotImplemented)
	}
}

// historyCall is mode=history: the listing, or with name=delete, removal.
func (s *Server) historyCall(params url.Values) any {
	switch params.Get("name") {
	case "":
		return s.historyListing(params)
	case actionDelete:
		ids := splitIDs(params.Get("value"))
		// SABnzbd 4 archives unless told archive=0, and Sonarr says
		// archive=0 only for a failed download
		archive := params.Get("archive") != "0"
		for _, j := range s.history(true) {
			if !ids.matchHistory(j) {
				continue
			}
			if archive {
				j.Archived = true
				if params.Get("del_files") == "1" {
					s.deleteFiles(j)
				}
				continue
			}
			s.drop(j, params.Get("del_files") == "1")
		}
		return statusAnswer{Status: true}
	default:
		return failure(msgNotImplemented)
	}
}

// queueListing is the queue, filtered and paged the way SabnzbdProxy.GetQueue
// asks: start and limit (0 is everything) and the category.
func (s *Server) queueListing(params url.Values) queueAnswer {
	var jobs []*Job
	for _, j := range s.queue() {
		if listed(j, params) {
			jobs = append(jobs, j)
		}
	}
	start, limit := page(params)
	total := len(jobs)
	jobs = window(jobs, start, limit)

	var size, left int64
	downloading := false
	slots := make([]queueSlot, 0, len(jobs))
	for i, j := range jobs {
		remaining := j.remaining()
		size += j.Size
		left += remaining
		downloading = downloading || j.Status == StatusDownloading
		slots = append(slots, queueSlot{
			Index:      start + i,
			NzoID:      j.ID,
			UnpackOpts: "3",
			Priority:   priorityName(j.Priority),
			Script:     noScript,
			Filename:   j.Name,
			Labels:     []string{},
			Password:   "",
			Cat:        j.Category,
			MBLeft:     megabytes(remaining),
			MB:         megabytes(j.Size),
			Size:       humanSize(j.Size),
			SizeLeft:   humanSize(remaining),
			Percentage: strconv.Itoa(int(j.Progress * 100)),
			MBMissing:  "0.0",
			Status:     string(j.Status),
			TimeLeft:   s.timeLeft(j),
			AvgAge:     "1d",
		})
	}

	status := "Idle"
	switch {
	case s.paused:
		status = string(StatusPaused)
	case downloading:
		status = string(StatusDownloading)
	}

	return queueAnswer{Queue: queue{
		Version:        s.version,
		Paused:         s.paused,
		PausedAll:      s.paused,
		Status:         status,
		SpeedLimit:     "100",
		Speed:          humanSize(s.speed),
		KBPerSec:       fmt.Sprintf("%.2f", float64(s.speed)/1024),
		Size:           humanSize(size),
		SizeLeft:       humanSize(left),
		MB:             megabytes(size),
		MBLeft:         megabytes(left),
		NoOfSlotsTotal: total,
		NoOfSlots:      len(slots),
		Start:          start,
		Limit:          limit,
		Finish:         start + len(slots),
		TimeLeft:       formatDuration(time.Duration(left/s.speed) * time.Second),
		Slots:          slots,
	}}
}

// historyListing is the history, newest first, filtered and paged the way
// SabnzbdProxy.GetHistory asks: start, limit (Sonarr's history limit, 60 by
// default) and the category.
func (s *Server) historyListing(params url.Values) historyAnswer {
	archived := params.Get("archive") == "1"
	var jobs []*Job
	for _, j := range s.history(true) {
		if j.Archived != archived || !listed(j, params) {
			continue
		}
		if params.Get("failed_only") == "1" && j.Status != StatusFailed {
			continue
		}
		jobs = append(jobs, j)
	}
	start, limit := page(params)
	total := len(jobs)
	jobs = window(jobs, start, limit)

	var totalBytes int64
	slots := make([]historySlot, 0, len(jobs))
	for _, j := range jobs {
		totalBytes += j.Size
		downloaded := int64(float64(j.Size) * j.Progress)
		retry := 0
		if j.Status == StatusFailed {
			retry = 1
		}
		slots = append(slots, historySlot{
			Completed:    j.Finished.Unix(),
			Name:         j.Name,
			NzbName:      cmp.Or(j.NZBName, j.Name+".nzb"),
			Category:     j.Category,
			PP:           "D",
			Script:       noScript,
			URL:          cmp.Or(j.URL, j.NZBName),
			Status:       string(j.Status),
			NzoID:        j.ID,
			Storage:      j.Storage,
			Path:         path.Join(s.incompleteDir(), folderName(j.Name)),
			DownloadTime: max(int(j.Finished.Sub(j.Added).Seconds()), 1),
			PostprocTime: 1,
			StageLog:     []string{},
			Downloaded:   downloaded,
			FailMessage:  j.FailMessage,
			Bytes:        downloaded,
			Size:         humanSize(downloaded),
			Retry:        retry,
			Archive:      j.Archived,
			TimeAdded:    j.Added.Unix(),
		})
	}

	return historyAnswer{History: history{
		NoOfSlots:         total,
		PPSlots:           0,
		DaySize:           humanSize(totalBytes),
		WeekSize:          humanSize(totalBytes),
		MonthSize:         humanSize(totalBytes),
		TotalSize:         humanSize(totalBytes),
		LastHistoryUpdate: time.Now().Unix(),
		Slots:             slots,
	}}
}

// addFile is mode=addfile: a job from the uploaded NZB, sized from its
// segments, named after nzbname or the file, in the category asked for.
func (s *Server) addFile(params url.Values, up upload) any {
	if up.data == nil {
		return failure(msgNoValue)
	}
	size, err := nzbSize(up.data)
	if err != nil {
		// SABnzbd answers what is not an NZB with no job and status false,
		// which Sonarr's CheckForError turns into a failed grab
		return addAnswer{Status: false, NzoIDs: []string{}}
	}
	name := params.Get("nzbname")
	if name == "" {
		name = trimNZB(path.Base(strings.ReplaceAll(up.filename, `\`, "/")))
	}

	j := s.add(name, params.Get("cat"), priority(params), size)
	j.NZB = up.data
	j.NZBName = up.filename

	return addAnswer{Status: true, NzoIDs: []string{j.ID}}
}

// addURL is mode=addurl: a job for the NZB at a URL, which SABnzbd would
// fetch itself. Sonarr downloads the NZB and uses addfile instead, so this
// only records where it was to come from.
func (s *Server) addURL(params url.Values) any {
	link := params.Get("name")
	if link == "" {
		return failure(msgNoValue)
	}
	name := params.Get("nzbname")
	if name == "" {
		name = "download"
		if u, err := url.Parse(link); err == nil && path.Base(u.Path) != "/" && path.Base(u.Path) != "." {
			name = trimNZB(path.Base(u.Path))
		}
	}

	j := s.add(name, params.Get("cat"), priority(params), DefaultSize)
	j.URL = link

	return addAnswer{Status: true, NzoIDs: []string{j.ID}}
}

// retry is mode=retry: a failed job back in the queue.
func (s *Server) retry(id string) any {
	j := s.find(id)
	if j == nil || j.Status != StatusFailed {
		return failure(msgNoItem)
	}
	j.Status, j.Progress, j.FailMessage, j.Finished, j.Archived = StatusQueued, 0, "", time.Time{}, false

	return retryAnswer{Status: true, NzoID: j.ID}
}

// drop removes a job, and with deleteFiles its folder. The caller holds the
// lock.
func (s *Server) drop(j *Job, deleteFiles bool) {
	if deleteFiles {
		s.deleteFiles(j)
	}
	for i, have := range s.jobs {
		if have == j {
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			return
		}
	}
}

// deleteFiles removes a finished job's folder, never anything outside the
// complete folder.
func (s *Server) deleteFiles(j *Job) {
	if j.HostPath == "" || s.hostCompleteDir == "" {
		return
	}
	if rel, err := filepath.Rel(s.hostCompleteDir, j.HostPath); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return
	}
	_ = os.RemoveAll(j.HostPath)
	j.HostPath = ""
}

// incompleteDir is where SABnzbd would download into, beside the complete
// folder.
func (s *Server) incompleteDir() string { return path.Join(path.Dir(s.completeDir), "incomplete") }

// timeLeft is a queued job's time to finish at the speed configured; nothing
// while it or the queue is paused.
func (s *Server) timeLeft(j *Job) string {
	if s.paused || j.Status == StatusPaused {
		return "0:00:00"
	}

	return formatDuration(time.Duration(j.remaining()/s.speed) * time.Second)
}

// remaining is what is left to download, in bytes.
func (j *Job) remaining() int64 {
	return j.Size - int64(float64(j.Size)*j.Progress)
}

// listed reports whether a job passes a listing's filters: category (cat on
// older clients), nzo_ids and search.
func listed(j *Job, params url.Values) bool {
	if c := cmp.Or(params.Get("category"), params.Get("cat")); c != "" && !strings.EqualFold(c, j.Category) {
		return false
	}
	if v := params.Get("nzo_ids"); v != "" && !splitIDs(v).match(j.ID) {
		return false
	}
	if q := params.Get("search"); q != "" && !strings.Contains(strings.ToLower(j.Name), strings.ToLower(q)) {
		return false
	}

	return true
}

// page reads start and limit; a limit of 0 is everything.
func page(params url.Values) (start, limit int) {
	start, _ = strconv.Atoi(params.Get("start"))
	limit, _ = strconv.Atoi(params.Get("limit"))

	return max(start, 0), max(limit, 0)
}

// window is jobs[start:start+limit], where limit 0 is everything.
func window(jobs []*Job, start, limit int) []*Job {
	if start >= len(jobs) {
		return nil
	}
	jobs = jobs[start:]
	if limit > 0 && limit < len(jobs) {
		jobs = jobs[:limit]
	}

	return jobs
}

// ids is the value= of an action: nzo_ids, comma-separated, or a word for
// every job ("all"), or in the history every failed or completed one.
type ids []string

func splitIDs(value string) ids {
	var out ids
	for id := range strings.SplitSeq(value, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}

	return out
}

func (v ids) match(id string) bool {
	for _, want := range v {
		if want == "all" || want == id {
			return true
		}
	}

	return false
}

func (v ids) matchHistory(j *Job) bool {
	for _, want := range v {
		switch {
		case want == "failed" && j.Status == StatusFailed,
			want == "completed" && j.Status == StatusCompleted:
			return true
		}
	}

	return v.match(j.ID)
}

// priority is addfile's priority= parameter; the category's default when
// absent.
func priority(params url.Values) int {
	p, err := strconv.Atoi(params.Get("priority"))
	if err != nil {
		return PriorityDefault
	}

	return p
}

// priorityName is how a queue slot shows a priority; Sonarr parses it back
// to its SabnzbdPriority by name.
func priorityName(p int) string {
	switch p {
	case PriorityForce:
		return "Force"
	case PriorityHigh:
		return "High"
	case PriorityLow:
		return "Low"
	case PriorityPaused:
		return "Paused"
	default:
		return "Normal"
	}
}

// trimNZB is a file name without its .nzb.
func trimNZB(name string) string {
	if strings.HasSuffix(strings.ToLower(name), ".nzb") {
		return name[:len(name)-len(".nzb")]
	}

	return name
}

// nzbSize checks data is an NZB - an nzb root with at least one file, what
// SABnzbd accepts - and adds up its segments' bytes.
func nzbSize(data []byte) (int64, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var size int64
	files, root := 0, ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("not an NZB: %w", err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if root == "" {
			root = el.Name.Local
		}
		switch el.Name.Local {
		case "file":
			files++
		case "segment":
			for _, a := range el.Attr {
				if a.Name.Local == "bytes" {
					n, _ := strconv.ParseInt(a.Value, 10, 64)
					size += n
				}
			}
		}
	}
	if root != "nzb" || files == 0 {
		return 0, errors.New("not an NZB: no nzb root with files")
	}

	return size, nil
}

// megabytes is a size in MiB, as the queue's mb and mbleft carry it: a
// string, which Sonarr reads as a decimal.
func megabytes(n int64) string {
	return fmt.Sprintf("%.2f", float64(n)/(1<<20))
}

// humanSize is a size as SABnzbd writes it for people, "1.2 GB".
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	value, suffix := float64(n)/unit, "KB"
	for _, next := range []string{"MB", "GB", "TB"} {
		if value < unit {
			break
		}
		value, suffix = value/unit, next
	}

	return fmt.Sprintf("%.1f %s", value, suffix)
}

// formatDuration is a time left as SABnzbd writes it, h:mm:ss, or d:hh:mm:ss
// past a day, the two shapes Sonarr's SabnzbdQueueTimeConverter reads.
func formatDuration(d time.Duration) string {
	total := int(d.Seconds())
	days, hours, minutes, seconds := total/86400, total/3600%24, total/60%60, total%60
	if days > 0 {
		return fmt.Sprintf("%d:%02d:%02d:%02d", days, hours, minutes, seconds)
	}

	return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
}

func emptyIfNil(v []string) []string {
	if v == nil {
		return []string{}
	}

	return v
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}

	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}
