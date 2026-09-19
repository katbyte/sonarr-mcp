package sabnzbd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testKey      = "sab-key"
	testCategory = "tv"
	containerDir = "/downloads/complete"
	release      = "Firefly.S01E07.Jaynestown.1080p.WEB-DL.DDP5.1.H.264-FAKE"
	episode      = "Show.S01E01"

	// the parameters the tests send
	keyArchive  = "archive"
	keyCat      = "cat"
	keyCategory = "category"
	keyDelFiles = "del_files"
	keyLimit    = "limit"

	// the priority Sonarr reads a slot with no other as
	normal = "Normal"
)

// The mirrors below read the answers the way Sonarr's SabnzbdProxy does,
// through Newtonsoft with Sonarr's settings: property names matched ignoring
// case, a number accepted from a string, a bool from 0 or 1, an enum by
// name ignoring case and nothing else. A shape Sonarr would not read fails
// these tests rather than a live run.

// lenientNumber is a C# decimal or int: a JSON number, or a string holding
// one, which is how SABnzbd sends mb, mbleft and percentage.
type lenientNumber float64

func (n *lenientNumber) UnmarshalJSON(b []byte) error {
	f, err := strconv.ParseFloat(strings.Trim(string(b), `"`), 64)
	if err != nil {
		return fmt.Errorf("not a number: %s", b)
	}
	*n = lenientNumber(f)

	return nil
}

// lenientBool is a C# bool: true or false, or an integer, which Newtonsoft
// converts; a string is an error.
type lenientBool bool

func (v *lenientBool) UnmarshalJSON(b []byte) error {
	switch s := string(b); s {
	case "true", "false":
		*v = s == "true"
	default:
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("not a bool: %s", b)
		}
		*v = n != 0
	}

	return nil
}

// lenientString is a C# string that SABnzbd may send as a bool, which
// Newtonsoft writes as True or False (SabnzbdJsonError.Status).
type lenientString string

func (v *lenientString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*v = lenientString(s)
		return nil
	}
	var flag bool
	if err := json.Unmarshal(b, &flag); err != nil {
		return fmt.Errorf("not a string: %s", b)
	}
	*v = "False"
	if flag {
		*v = "True"
	}

	return nil
}

// downloadStatus is SabnzbdDownloadStatus, read by name.
type downloadStatus string

var downloadStatuses = []string{
	"Grabbing", string(StatusQueued), string(StatusPaused), "Checking", string(StatusDownloading), "QuickCheck", "Verifying", "Repairing",
	"Fetching", "Extracting", "Moving", "Running", string(StatusCompleted), string(StatusFailed), "Deleted", "Propagating",
}

func (v *downloadStatus) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	i := slices.IndexFunc(downloadStatuses, func(name string) bool { return strings.EqualFold(name, s) })
	if i < 0 {
		return fmt.Errorf("status %q is not a SabnzbdDownloadStatus", s)
	}
	*v = downloadStatus(downloadStatuses[i])

	return nil
}

// queueTime is SabnzbdQueueTimeConverter: h:m:s or d:h:m:s.
type queueTime time.Duration

func (v *queueTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	var parts []int
	for p := range strings.SplitSeq(s, ":") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return fmt.Errorf("timeleft %q: %w", s, err)
		}
		parts = append(parts, n)
	}
	switch len(parts) {
	case 4:
		*v = queueTime(time.Duration((parts[0]*24+parts[1])*3600+parts[2]*60+parts[3]) * time.Second)
	case 3:
		*v = queueTime(time.Duration(parts[0]*3600+parts[1]*60+parts[2]) * time.Second)
	default:
		return fmt.Errorf("timeleft %q is neither 0:0:0 nor 0:0:0:0", s)
	}

	return nil
}

// sabPriority is SabnzbdPriorityTypeConverter: Enum.TryParse, Normal when
// the name is unknown.
type sabPriority int

func (v *sabPriority) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	names := map[string]sabPriority{"Default": -100, string(StatusPaused): -2, "Low": -1, normal: 0, "High": 1, "Force": 2}
	*v = names[s]

	return nil
}

type jsonErrorMirror struct {
	Status lenientString `json:"status"`
	Error  string        `json:"error"`
}

func (e *jsonErrorMirror) failed() bool { return strings.EqualFold(string(e.Status), "false") }

type versionMirror struct {
	Version string `json:"version"`
}

type configMirror struct {
	Config struct {
		Misc struct {
			CompleteDir            string        `json:"complete_dir"`
			TvCategories           []string      `json:"tv_categories"`
			EnableTvSorting        lenientBool   `json:"enable_tv_sorting"`
			MovieCategories        []string      `json:"movie_categories"`
			EnableMovieSorting     lenientBool   `json:"enable_movie_sorting"`
			EnableDateSorting      lenientBool   `json:"enable_date_sorting"`
			PreCheck               lenientBool   `json:"pre_check"`
			HistoryRetention       string        `json:"history_retention"`
			HistoryRetentionOption string        `json:"history_retention_option"`
			HistoryRetentionNumber lenientNumber `json:"history_retention_number"`
		} `json:"misc"`
		Categories []struct {
			Priority lenientNumber `json:"priority"`
			PP       string        `json:"pp"`
			Name     string        `json:"name"`
			Script   string        `json:"script"`
			Dir      string        `json:"dir"`
		} `json:"categories"`
		Servers []json.RawMessage `json:"servers"`
		Sorters []struct {
			Name     string      `json:"name"`
			SortCats []string    `json:"sort_cats"`
			IsActive lenientBool `json:"is_active"`
		} `json:"sorters"`
	} `json:"config"`
}

type fullStatusMirror struct {
	Status struct {
		CompleteDir string `json:"completedir"`
	} `json:"status"`
}

type addMirror struct {
	Status bool     `json:"status"`
	IDs    []string `json:"nzo_ids"`
}

type retryMirror struct {
	Status bool   `json:"status"`
	ID     string `json:"nzo_id"`
}

type queueMirror struct {
	Queue struct {
		Paused bool `json:"paused"`
		Items  []struct {
			Status     downloadStatus `json:"status"`
			Index      int            `json:"index"`
			Timeleft   queueTime      `json:"timeleft"`
			Size       lenientNumber  `json:"mb"`
			Title      string         `json:"filename"`
			Priority   sabPriority    `json:"priority"`
			Category   string         `json:"cat"`
			Sizeleft   lenientNumber  `json:"mbleft"`
			Percentage lenientNumber  `json:"percentage"`
			ID         string         `json:"nzo_id"`
		} `json:"slots"`
	} `json:"queue"`
}

type historyMirror struct {
	History struct {
		Paused bool `json:"paused"`
		Items  []struct {
			FailMessage  string         `json:"fail_message"`
			Size         int64          `json:"bytes"`
			Category     string         `json:"category"`
			NzbName      string         `json:"nzb_name"`
			DownloadTime int            `json:"download_time"`
			Storage      string         `json:"storage"`
			Status       downloadStatus `json:"status"`
			ID           string         `json:"nzo_id"`
			Title        string         `json:"name"`
		} `json:"slots"`
	} `json:"history"`
}

// values builds query parameters from name, value pairs, in the order given.
func values(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Add(pairs[i], pairs[i+1])
	}

	return v
}

func newServer(t *testing.T, opts Options) *Server {
	t.Helper()

	if opts.APIKey == "" {
		opts.APIKey = testKey
	}
	if opts.CompleteDir == "" {
		opts.CompleteDir = containerDir
	}
	if opts.HostCompleteDir == "" {
		opts.HostCompleteDir = t.TempDir()
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

// call sends one API call the way SabnzbdProxy.BuildRequest does - mode
// first, the key and output=json last - and decodes the answer into into,
// after Sonarr's CheckForError has had its look. It returns the error
// CheckForError would throw with, or "".
func call(t *testing.T, s *Server, mode string, params url.Values, into any) string {
	t.Helper()

	return send(t, http.MethodGet, apiURL(s, mode, params, testKey), nil, "", into)
}

func apiURL(s *Server, mode string, params url.Values, key string) string {
	q := "mode=" + url.QueryEscape(mode)
	if len(params) > 0 {
		q += "&" + params.Encode()
	}
	if key != "" {
		q += "&apikey=" + url.QueryEscape(key)
	}

	return s.LocalURL() + "/api?" + q + "&output=json"
}

func send(t *testing.T, method, target string, body []byte, contentType string, into any) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%s %s = %d %s: %s", method, target, resp.StatusCode, resp.Header.Get("Content-Type"), raw)
	}

	// CheckForError: an object whose status is false is a failure
	var e jsonErrorMirror
	if json.Unmarshal(raw, &e) == nil && e.failed() {
		return "Error response received from SABnzbd: " + e.Error
	}
	if into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("Sonarr could not read %s: %v\n%s", target, err, raw)
		}
	}

	return ""
}

// addFile uploads an NZB exactly as SabnzbdProxy.DownloadNzb does: a POST
// with cat and priority in the query and the file in a multipart part named
// "name", framed the way HttpRequestBuilder.ApplyFormData writes it.
func addFile(t *testing.T, s *Server, filename string, nzb []byte) addMirror {
	t.Helper()

	boundary := "-----------------------------" + fmt.Sprintf("%014x", time.Now().UnixNano())
	var body bytes.Buffer
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(`Content-Disposition: form-data; name="name"; filename="` + filename + `"` + "\r\n")
	body.WriteString("Content-Type: application/x-nzb\r\n\r\n")
	body.Write(nzb)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	var answer addMirror
	target := apiURL(s, modeAddFile, values(keyCat, testCategory, "priority", "-100"), testKey)
	if msg := send(t, http.MethodPost, target, body.Bytes(), "multipart/form-data; boundary="+boundary, &answer); msg != "" {
		t.Fatalf("addfile: %s", msg)
	}

	return answer
}

// nzb is an NZB of the shape the fake indexer serves: files whose segments
// add up to size.
func nzb(size int64, files int) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">` + "\n")
	b.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">` + "\n")
	for i := range files {
		fmt.Fprintf(&b, `<file poster="p" date="1" subject="s%d"><groups><group>a.b.c</group></groups><segments>`, i)
		fmt.Fprintf(&b, `<segment bytes="%d" number="1">m%d@x</segment></segments></file>`+"\n", size/int64(files), i)
	}
	b.WriteString("</nzb>\n")

	return []byte(b.String())
}

func queueOf(t *testing.T, s *Server, params url.Values) queueMirror {
	t.Helper()

	var q queueMirror
	if msg := call(t, s, modeQueue, params, &q); msg != "" {
		t.Fatal(msg)
	}

	return q
}

func historyOf(t *testing.T, s *Server, params url.Values) historyMirror {
	t.Helper()

	var h historyMirror
	if msg := call(t, s, modeHistory, params, &h); msg != "" {
		t.Fatal(msg)
	}

	return h
}

// Sonarr's Sabnzbd.Test, run against the fake: every check it makes of a new
// client passes.
func TestSonarrAcceptsTheClient(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{})

	// TestConnectionAndVersion
	var v versionMirror
	if msg := call(t, s, modeVersion, nil, &v); msg != "" {
		t.Fatal(msg)
	}
	m := regexp.MustCompile(`(?P<major>\d+)\.(?P<minor>\d+)\.(?P<patch>\d+|x)`).FindStringSubmatch(v.Version)
	if m == nil {
		t.Fatalf("version %q does not parse", v.Version)
	}
	if major, _ := strconv.Atoi(m[1]); major < 1 {
		t.Errorf("version %q is older than Sonarr accepts", v.Version)
	}

	// TestAuthentication, TestGlobalConfig, TestCategory
	var c configMirror
	if msg := call(t, s, modeGetConfig, nil, &c); msg != "" {
		t.Fatal(msg)
	}
	misc := c.Config.Misc
	if bool(misc.PreCheck) {
		t.Error("pre_check is on")
	}
	if !path.IsAbs(misc.CompleteDir) {
		t.Fatalf("complete_dir %q is not rooted, so Sonarr would ask fullstatus", misc.CompleteDir)
	}
	var tv *string
	for i := range c.Config.Categories {
		if c.Config.Categories[i].Name == testCategory {
			tv = &c.Config.Categories[i].Dir
		}
	}
	if tv == nil || strings.HasSuffix(*tv, "*") {
		t.Fatalf("categories = %+v: Sonarr's category is missing or has no job folders", c.Config.Categories)
	}
	for _, sorter := range c.Config.Sorters {
		if bool(sorter.IsActive) && (len(sorter.SortCats) == 0 || slices.Contains(sorter.SortCats, testCategory)) {
			t.Errorf("sorter %s sorts Sonarr's category", sorter.Name)
		}
	}
	if bool(misc.EnableTvSorting || misc.EnableMovieSorting || misc.EnableDateSorting) {
		t.Error("a legacy sorting switch is on")
	}

	// GetStatus: the category's folder is where Sonarr expects downloads,
	// and RemovesCompletedDownloads is false, so no health check fires
	if output := path.Join(misc.CompleteDir, *tv); output != containerDir {
		t.Errorf("output root = %s", output)
	}
	if misc.HistoryRetentionOption != "all" {
		t.Errorf("history retention %q would have Sonarr warn that completed downloads are removed", misc.HistoryRetentionOption)
	}

	var status fullStatusMirror
	if msg := call(t, s, modeFullStatus, values("skip_dashboard", "1"), &status); msg != "" || status.Status.CompleteDir != containerDir {
		t.Errorf("fullstatus = %+v %s", status, msg)
	}
	var cats struct {
		Categories []string `json:"categories"`
	}
	if msg := call(t, s, modeGetCats, nil, &cats); msg != "" || !slices.Equal(cats.Categories, []string{"*", testCategory}) {
		t.Errorf("get_cats = %v %s", cats, msg)
	}
}

func TestAuth(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{})
	// version answers without a key
	var v versionMirror
	if msg := send(t, http.MethodGet, apiURL(s, modeVersion, nil, ""), nil, "", &v); msg != "" || v.Version != DefaultVersion {
		t.Errorf("version with no key = %+v %s", v, msg)
	}
	// Sonarr's TestAuthentication looks for these words
	if msg := send(t, http.MethodGet, apiURL(s, modeGetConfig, nil, ""), nil, "", nil); !strings.Contains(msg, "API Key Required") {
		t.Errorf("no key = %q", msg)
	}
	if msg := send(t, http.MethodGet, apiURL(s, modeGetConfig, nil, "nope"), nil, "", nil); !strings.Contains(msg, "API Key Incorrect") {
		t.Errorf("wrong key = %q", msg)
	}

	open, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = open.Close() }()
	if msg := send(t, http.MethodGet, apiURL(open, modeGetConfig, nil, ""), nil, "", nil); msg != "" {
		t.Errorf("a client with no key refused a call: %s", msg)
	}
}

// A grab, as Sonarr makes one, through to an import and the cleanup after.
func TestLifecycle(t *testing.T) {
	t.Parallel()

	host := t.TempDir()
	s := newServer(t, Options{HostCompleteDir: host, Speed: 1 << 20})
	added := addFile(t, s, release+".nzb", nzb(4<<30, 2))
	if !added.Status || len(added.IDs) != 1 || !strings.HasPrefix(added.IDs[0], "SABnzbd_nzo_") {
		t.Fatalf("addfile = %+v", added)
	}
	id := added.IDs[0]

	// queued: GetQueue(0, 0) with the category
	q := queueOf(t, s, values("start", "0", keyLimit, "0", keyCategory, testCategory))
	if len(q.Queue.Items) != 1 || q.Queue.Paused {
		t.Fatalf("queue = %+v", q)
	}
	item := q.Queue.Items[0]
	if item.ID != id || item.Title != release || item.Category != testCategory || item.Status != "Queued" || item.Priority != 0 {
		t.Errorf("queued item = %+v", item)
	}
	// Sonarr's TotalSize is mb * 1024 * 1024
	if total := int64(float64(item.Size) * 1024 * 1024); total != 4<<30 || item.Sizeleft != item.Size {
		t.Errorf("sizes = %v mb, %v left", item.Size, item.Sizeleft)
	}

	// downloading, half way, 2 GiB left at 1 MiB/s
	if err := s.SetProgress(id, 0.5); err != nil {
		t.Fatal(err)
	}
	item = queueOf(t, s, nil).Queue.Items[0]
	if item.Status != downloadStatus(StatusDownloading) || item.Percentage != 50 || item.Sizeleft != 2048 || time.Duration(item.Timeleft) != 2048*time.Second {
		t.Errorf("downloading item = %+v", item)
	}

	// paused, and back
	if err := s.Pause(id); err != nil {
		t.Fatal(err)
	}
	if item = queueOf(t, s, nil).Queue.Items[0]; item.Status != downloadStatus(StatusPaused) || item.Timeleft != 0 {
		t.Errorf("paused item = %+v", item)
	}
	if err := s.Resume(id); err != nil {
		t.Fatal(err)
	}
	if item = queueOf(t, s, nil).Queue.Items[0]; item.Status != downloadStatus(StatusDownloading) {
		t.Errorf("resumed item = %+v", item)
	}

	// complete with a file, which lands where the container will look
	video := []byte("not really a video")
	if err := s.Complete(id, map[string][]byte{release + ".mkv": video, "Sample/sample.mkv": []byte("s")}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(host, release, release+".mkv")); err != nil || !bytes.Equal(got, video) { //nolint:gosec // the test's own temp dir
		t.Errorf("the finished file = %q, %v", got, err)
	}
	if fi, err := os.Stat(filepath.Join(host, release)); err != nil || fi.Mode().Perm() != 0o777 {
		t.Errorf("the job folder = %v, %v; the container must be able to move out of it", fi, err)
	}
	if len(queueOf(t, s, nil).Queue.Items) != 0 {
		t.Error("a completed job is still queued")
	}
	h := historyOf(t, s, values("start", "0", keyLimit, "60", keyCategory, testCategory))
	if len(h.History.Items) != 1 {
		t.Fatalf("history = %+v", h)
	}
	done := h.History.Items[0]
	if done.ID != id || done.Status != "Completed" || done.Storage != containerDir+"/"+release || done.Title != release ||
		done.Size != 4<<30 || done.NzbName != release+".nzb" || done.Category != testCategory || done.DownloadTime < 1 {
		t.Errorf("history item = %+v", done)
	}

	// Sonarr removes an imported download from the history, archiving it
	if msg := call(t, s, modeHistory, values("name", actionDelete, keyDelFiles, "1", "value", id, keyArchive, "1"), nil); msg != "" {
		t.Fatal(msg)
	}
	if _, err := os.Stat(filepath.Join(host, release)); !os.IsNotExist(err) {
		t.Errorf("del_files left the folder: %v", err)
	}
	if h = historyOf(t, s, nil); len(h.History.Items) != 0 {
		t.Errorf("an archived job is still listed: %+v", h)
	}
	if h = historyOf(t, s, values(keyArchive, "1")); len(h.History.Items) != 1 {
		t.Errorf("the archive = %+v", h)
	}
	job, ok := s.Job(id)
	if !ok || !job.Archived || job.HostPath != "" || !bytes.Equal(job.NZB, nzb(4<<30, 2)) {
		t.Errorf("job after archiving = %+v", job)
	}

	// archive=0 deletes for good
	if msg := call(t, s, modeHistory, values("name", actionDelete, "value", id, keyArchive, "0"), nil); msg != "" {
		t.Fatal(msg)
	}
	if _, ok := s.Job(id); ok {
		t.Error("archive=0 kept the job")
	}
}

func TestFailAndRetry(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{})
	id := addFile(t, s, release+".nzb", nzb(1<<30, 1)).IDs[0]
	if err := s.Fail(id, ""); err != nil {
		t.Fatal(err)
	}
	h := historyOf(t, s, values(keyCategory, testCategory))
	if len(h.History.Items) != 1 || h.History.Items[0].Status != "Failed" || h.History.Items[0].FailMessage != defaultFailMessage || h.History.Items[0].Storage != "" {
		t.Fatalf("failed = %+v", h)
	}
	if h = historyOf(t, s, values("failed_only", "1")); len(h.History.Items) != 1 {
		t.Errorf("failed_only = %+v", h)
	}

	var r retryMirror
	if msg := call(t, s, modeRetry, values("value", id), &r); msg != "" || !r.Status || r.ID != id {
		t.Fatalf("retry = %+v %s", r, msg)
	}
	if q := queueOf(t, s, nil); len(q.Queue.Items) != 1 || q.Queue.Items[0].Status != "Queued" {
		t.Errorf("retried = %+v", q)
	}
	// a retry of anything but a failed job is refused
	if msg := call(t, s, modeRetry, values("value", id), nil); !strings.Contains(msg, "item does not exist") {
		t.Errorf("retry of a queued job = %q", msg)
	}

	if err := s.Fail(id, "Unpacking failed, write error or disk is full?"); err != nil {
		t.Fatal(err)
	}
	if h = historyOf(t, s, nil); h.History.Items[0].FailMessage != "Unpacking failed, write error or disk is full?" {
		t.Errorf("fail message = %q", h.History.Items[0].FailMessage)
	}
	// a failed download is removed with archive=0, as Sonarr does
	if msg := call(t, s, modeHistory, values("name", actionDelete, "value", "failed", keyDelFiles, "1", keyArchive, "0"), nil); msg != "" {
		t.Fatal(msg)
	}
	if len(s.Jobs()) != 0 {
		t.Errorf("jobs after deleting the failed = %+v", s.Jobs())
	}
}

func TestQueueActions(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{})
	a := s.AddJob("Show.S01E01.720p.HDTV.x264-FAKE", testCategory)
	b := s.AddJob("Show.S01E02.720p.HDTV.x264-FAKE", testCategory)
	c := s.AddJob("Movie.2024.1080p.WEB-DL-FAKE", "movies")
	if a.Size != DefaultSize || c.Category != "*" || a.Status != StatusQueued {
		t.Errorf("added = %+v, %+v", a, c)
	}

	// pause and resume one job through the API
	var answer addMirror
	if msg := call(t, s, modeQueue, values("name", modePause, "value", a.ID), &answer); msg != "" || !slices.Equal(answer.IDs, []string{a.ID}) {
		t.Errorf("pause = %+v %s", answer, msg)
	}
	if j, _ := s.Job(a.ID); j.Status != StatusPaused {
		t.Errorf("paused = %s", j.Status)
	}
	if msg := call(t, s, modeQueue, values("name", modeResume, "value", a.ID), &answer); msg != "" {
		t.Error(msg)
	}
	if j, _ := s.Job(a.ID); j.Status != StatusQueued {
		t.Errorf("resumed = %s", j.Status)
	}

	// the whole queue paused: Sonarr sees every item paused
	if msg := call(t, s, modePause, nil, nil); msg != "" {
		t.Fatal(msg)
	}
	if q := queueOf(t, s, nil); !q.Queue.Paused {
		t.Error("the queue is not paused")
	}
	s.PauseAll(false)
	if q := queueOf(t, s, nil); q.Queue.Paused {
		t.Error("the queue is still paused")
	}

	// delete two, one by the API's comma list
	if msg := call(t, s, modeQueue, values("name", actionDelete, "value", a.ID+","+c.ID, keyDelFiles, "0"), &answer); msg != "" || len(answer.IDs) != 2 {
		t.Errorf("delete = %+v %s", answer, msg)
	}
	if jobs := s.Jobs(); len(jobs) != 1 || jobs[0].ID != b.ID {
		t.Errorf("after delete = %+v", jobs)
	}
	if msg := call(t, s, modeQueue, values("name", actionDelete, "value", "all"), &answer); msg != "" || len(answer.IDs) != 1 {
		t.Errorf("delete all = %+v %s", answer, msg)
	}
	// deleting what is not there answers no ids, and a history delete of
	// nothing changes nothing
	if msg := call(t, s, modeQueue, values("name", actionDelete, "value", "SABnzbd_nzo_gone"), &answer); msg != "" || answer.IDs == nil || len(answer.IDs) != 0 {
		t.Errorf("delete of nothing = %+v %s", answer, msg)
	}
	if msg := call(t, s, modeHistory, values("name", actionDelete, "value", "completed", keyArchive, "0"), nil); msg != "" {
		t.Error(msg)
	}
	if msg := call(t, s, modeQueue, values("name", "sort"), nil); msg == "" {
		t.Error("an unknown queue action succeeded")
	}
	if msg := call(t, s, modeHistory, values("name", "mark_as_completed"), nil); msg == "" {
		t.Error("an unknown history action succeeded")
	}
}

func TestListings(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{})
	ids := make([]string, 0, 5)
	for i := range 5 {
		ids = append(ids, s.AddJob(fmt.Sprintf("Show.S01E%02d.720p.HDTV.x264-FAKE", i+1), testCategory).ID)
	}
	other := s.AddJob("Other.Thing", "anime")

	tests := []struct {
		name   string
		params url.Values
		want   []string
	}{
		{"everything", values("start", "0", keyLimit, "0"), append(slices.Clone(ids), other.ID)},
		{"the category", values(keyCategory, testCategory), ids},
		{"cat, the older spelling", values(keyCat, "*"), []string{other.ID}},
		{"a page", values("start", "1", keyLimit, "2", keyCategory, testCategory), ids[1:3]},
		{"past the end", values("start", "9"), []string{}},
		{"by id", values("nzo_ids", ids[3]+","+ids[0]), []string{ids[0], ids[3]}},
		{"by search", values("search", "s01e05"), ids[4:]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			q := queueOf(t, s, tt.params)
			got := []string{}
			for _, item := range q.Queue.Items {
				got = append(got, item.ID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("queue = %v, want %v", got, tt.want)
			}
		})
	}

	// the history is newest first, and pages the same way
	h := newServer(t, Options{})
	done := make([]string, 0, 3)
	for i := range 3 {
		j := h.AddJob(fmt.Sprintf("Done.%d", i), testCategory)
		if err := h.Complete(j.ID, map[string][]byte{"f.mkv": {1}}); err != nil {
			t.Fatal(err)
		}
		done = append(done, j.ID)
		time.Sleep(2 * time.Millisecond)
	}
	got := historyOf(t, h, values("start", "0", keyLimit, "2", keyCategory, testCategory))
	if len(got.History.Items) != 2 || got.History.Items[0].ID != done[2] || got.History.Items[1].ID != done[1] {
		t.Errorf("history page = %+v", got)
	}
}

func TestAddVariants(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{})

	// nzbname overrides the file's name, and a paused priority starts paused
	var answer addMirror
	boundary := "b"
	body := "--b\r\nContent-Disposition: form-data; name=\"nzbfile\"; filename=\"x.NZB\"\r\n\r\n" + string(nzb(10, 1)) + "\r\n--b--\r\n"
	target := apiURL(s, modeAddFile, values(keyCat, testCategory, "priority", "-2", "nzbname", "Named"), testKey)
	if msg := send(t, http.MethodPost, target, []byte(body), "multipart/form-data; boundary="+boundary, &answer); msg != "" {
		t.Fatal(msg)
	}
	j, ok := s.FindJob("named")
	if !ok || j.Status != StatusPaused || j.Size != 10 || j.NZBName != "x.NZB" || j.ID != answer.IDs[0] {
		t.Errorf("named job = %+v", j)
	}
	if _, ok := s.FindJob("nothing like it"); ok {
		t.Error("FindJob found a job that is not there")
	}

	// what is not an NZB is refused with no job and status false, which
	// Sonarr's CheckForError throws on
	before := len(s.Jobs())
	for name, body := range map[string]string{"bad.nzb": "<html>nope</html>", "broken.nzb": "<nzb><file>"} {
		upload := "--b\r\nContent-Disposition: form-data; name=\"name\"; filename=\"" + name + "\"\r\nContent-Type: application/x-nzb\r\n\r\n" + body + "\r\n--b--\r\n"
		if msg := send(t, http.MethodPost, apiURL(s, modeAddFile, nil, testKey), []byte(upload), "multipart/form-data; boundary=b", nil); !strings.HasPrefix(msg, "Error response received from SABnzbd") {
			t.Errorf("%s = %q, want a failure", name, msg)
		}
	}
	if len(s.Jobs()) != before {
		t.Error("a job was made from something that is not an NZB")
	}
	// a POST with no file
	if msg := send(t, http.MethodPost, apiURL(s, modeAddFile, nil, testKey), []byte("--b--\r\n"), "multipart/form-data; boundary=b", nil); !strings.Contains(msg, "expects one parameter") {
		t.Errorf("addfile with no file = %q", msg)
	}
	// a multipart body that does not parse
	if msg := send(t, http.MethodPost, apiURL(s, modeAddFile, nil, testKey), []byte("junk"), "multipart/form-data; boundary=b", nil); !strings.Contains(msg, "reading the upload") {
		t.Errorf("a broken upload = %q", msg)
	}

	// addurl takes its name from the link
	if msg := call(t, s, modeAddURL, values("name", "http://indexer/getnzb/Show.S01E03.nzb?i=1", keyCat, testCategory), &answer); msg != "" || !answer.Status {
		t.Fatalf("addurl = %+v %s", answer, msg)
	}
	if j, ok := s.Job(answer.IDs[0]); !ok || j.Name != "Show.S01E03" || j.URL == "" {
		t.Errorf("addurl job = %+v", j)
	}
	if msg := call(t, s, modeAddURL, values("name", "http://indexer/"), &answer); msg != "" {
		t.Error(msg)
	}
	if j, _ := s.Job(answer.IDs[0]); j.Name != "download" {
		t.Errorf("addurl with no file name = %q", j.Name)
	}
	if msg := call(t, s, modeAddURL, nil, nil); !strings.Contains(msg, "expects one parameter") {
		t.Errorf("addurl with no url = %q", msg)
	}
}

func TestHooks(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{})
	j := s.AddJob("Hooked", testCategory)
	if err := s.SetSize(j.ID, 42); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Job(j.ID); got.Size != 42 {
		t.Errorf("size = %d", got.Size)
	}

	// every hook refuses an unknown job
	for name, hook := range map[string]error{
		"SetSize":     s.SetSize("nope", 1),
		"SetProgress": s.SetProgress("nope", 1),
		"Pause":       s.Pause("nope"),
		"Resume":      s.Resume("nope"),
		"Complete":    s.Complete("nope", nil),
		"Fail":        s.Fail("nope", ""),
		"Remove":      s.Remove("nope"),
	} {
		if !errors.Is(hook, ErrNoJob) {
			t.Errorf("%s of an unknown job = %v", name, hook)
		}
	}

	// progress is clamped, and a finished job cannot be moved again
	if err := s.SetProgress(j.ID, 7); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Job(j.ID); got.Progress != 1 {
		t.Errorf("progress = %v", got.Progress)
	}
	if err := s.CompleteWith(j.ID, func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "made.mkv"), []byte("x"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"SetProgress": s.SetProgress(j.ID, 0.1),
		"Pause":       s.Pause(j.ID),
		"Resume":      s.Resume(j.ID),
		"Complete":    s.Complete(j.ID, nil),
		"Fail":        s.Fail(j.ID, "late"),
	} {
		if !errors.Is(err, ErrWrongState) {
			t.Errorf("%s of a completed job = %v", name, err)
		}
	}
	if err := s.Remove(j.ID); err != nil {
		t.Fatal(err)
	}

	// a fill that fails leaves the job queued, and files may not escape
	k := s.AddJob("../../escape", testCategory)
	if err := s.CompleteWith(k.ID, func(string) error { return errors.New("disk full") }); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("a failed fill = %v", err)
	}
	if got, _ := s.Job(k.ID); got.Status != StatusQueued {
		t.Errorf("after a failed fill = %s", got.Status)
	}
	if err := s.Complete(k.ID, map[string][]byte{"../outside": {1}}); err == nil {
		t.Error("a file outside the job's folder was written")
	}

	// with nowhere to write, completing is an error
	bare, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bare.Close() }()
	if err := bare.Complete(bare.AddJob("x", testCategory).ID, nil); err == nil {
		t.Error("Complete with no HostCompleteDir succeeded")
	}
}

func TestCallsAndRouting(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{})
	call(t, s, modeVersion, nil, nil)
	addFile(t, s, release+".nzb", nzb(1, 1))
	call(t, s, modeQueue, values("start", "0", keyLimit, "0"), nil)

	calls := s.Calls()
	if len(calls) != 3 || calls[0].Mode != modeVersion || calls[1].Method != http.MethodPost || calls[1].File != release+".nzb" ||
		calls[1].Params.Get("cat") != testCategory || calls[1].Params.Get("priority") != "-100" || calls[2].Params.Get("limit") != "0" {
		t.Fatalf("calls = %+v", calls)
	}
	if got := s.CallsFor(modeAddFile); len(got) != 1 || got[0].Params.Get("output") != "json" || got[0].Time.IsZero() {
		t.Errorf("addfile calls = %+v", got)
	}
	s.ResetCalls()
	if len(s.Calls()) != 0 {
		t.Error("ResetCalls kept the calls")
	}

	if msg := call(t, s, "warnings", nil, nil); !strings.Contains(msg, "not implemented") {
		t.Errorf("an unknown mode = %q", msg)
	}
	if msg := send(t, http.MethodGet, s.LocalURL()+"/api?apikey="+testKey+"&output=json", nil, "", nil); !strings.Contains(msg, "not implemented") {
		t.Errorf("no mode = %q", msg)
	}
	// a form that does not parse
	if msg := send(t, http.MethodPost, s.LocalURL()+"/api", []byte("%zz"), "application/x-www-form-urlencoded", nil); !strings.Contains(msg, "reading the form") {
		t.Errorf("a broken form = %q", msg)
	}
	// a URL base in front of /api still reaches the API
	var v versionMirror
	if msg := send(t, http.MethodGet, s.LocalURL()+"/sabnzbd/api?mode=version&output=json", nil, "", &v); msg != "" || v.Version != DefaultVersion {
		t.Errorf("/sabnzbd/api = %+v %s", v, msg)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.LocalURL()+"/nope", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/nope = %d", resp.StatusCode)
	}
}

func TestAddressesAndFormats(t *testing.T) {
	t.Parallel()

	s := newServer(t, Options{PublicHost: "host.docker.internal", Category: "sonarr"})
	if s.Host() != "host.docker.internal" || s.URL() != "http://host.docker.internal:"+strconv.Itoa(s.Port()) ||
		s.LocalURL() != "http://127.0.0.1:"+strconv.Itoa(s.Port()) || !strings.HasSuffix(s.Addr(), ":"+strconv.Itoa(s.Port())) || s.Category() != "sonarr" {
		t.Errorf("Host %s URL %s LocalURL %s Addr %s Category %s", s.Host(), s.URL(), s.LocalURL(), s.Addr(), s.Category())
	}
	if _, err := New(Options{Addr: "256.0.0.1:0"}); err == nil {
		t.Error("New on an address that cannot be listened on succeeded")
	}

	for in, want := range map[time.Duration]string{0: "0:00:00", 59 * time.Second: "0:00:59", 3723 * time.Second: "1:02:03", 90061 * time.Second: "1:01:01:01"} {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1536: "1.5 KB", 5 << 20: "5.0 MB", 3 << 30: "3.0 GB", 2 << 40: "2.0 TB"} {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[int]string{PriorityForce: "Force", PriorityHigh: "High", PriorityNormal: normal, PriorityLow: "Low", PriorityPaused: "Paused", PriorityDefault: normal} {
		if got := priorityName(in); got != want {
			t.Errorf("priorityName(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"a/b": "a_b", `a\b`: "a_b", " ": "job", "..": "job", episode: episode} {
		if got := folderName(in); got != want {
			t.Errorf("folderName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := trimNZB(episode + ".Nzb"); got != episode {
		t.Errorf("trimNZB = %q", got)
	}
	if got := trimNZB(episode + ".txt"); got != episode+".txt" {
		t.Errorf("trimNZB of another file = %q", got)
	}
	if id := newID(); !regexp.MustCompile(`^SABnzbd_nzo_[a-z0-9]{8}$`).MatchString(id) || id == newID() {
		t.Errorf("newID = %q", id)
	}
}
