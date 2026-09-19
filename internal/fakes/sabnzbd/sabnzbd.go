// Package sabnzbd is a fake SABnzbd for the live suites.
//
// Sonarr adds it as its usenet download client, hands it the NZB of every
// release it grabs, and polls its queue and history every minute
// (RefreshMonitoredDownloads) to track each download and import the finished
// ones. Every answer is served in the shape Sonarr's SabnzbdProxy
// deserializes (SabnzbdQueue, SabnzbdHistory, SabnzbdConfig in Sonarr's
// source), and the configuration it reports passes Sonarr's own checks of a
// new client: a recent version, the category present with job folders, no
// sorting on the category, history kept.
//
// Nothing downloads. A test decides what becomes of each job - progress,
// paused, completed with the files it names, failed with a message - and the
// files of a completed job are written under HostCompleteDir, the folder the
// container sees as CompleteDir, where Sonarr goes to import them.
package sabnzbd

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status is a job's state, as SABnzbd names it in the queue and history.
type Status string

// The states a job moves through. Queued, Downloading and Paused are in the
// queue; Completed and Failed in the history.
const (
	StatusQueued      Status = "Queued"
	StatusDownloading Status = "Downloading"
	StatusPaused      Status = "Paused"
	StatusCompleted   Status = "Completed"
	StatusFailed      Status = "Failed"
)

// SABnzbd's priorities, as addfile takes them. Default is the category's,
// which Sonarr sends unless its client is set otherwise; a job added Paused
// starts paused.
const (
	PriorityDefault = -100
	PriorityPaused  = -2
	PriorityLow     = -1
	PriorityNormal  = 0
	PriorityHigh    = 1
	PriorityForce   = 2
)

// DefaultVersion is the SABnzbd version reported: a 4.x, which passes
// Sonarr's version check and has the sorters it looks at.
const DefaultVersion = "4.5.1"

// DefaultSize is the size of a job added with AddJob, which has no NZB to
// size it from.
const DefaultSize = 1 << 30

// defaultFailMessage is what SABnzbd says of a download it gave up on.
const defaultFailMessage = "Aborted, cannot be completed - https://sabnzbd.org/not-complete"

// Job is one download.
type Job struct {
	// ID is SABnzbd's nzo_id, the download id Sonarr tracks the grab by.
	ID string
	// Name is the job's name: the nzbname given, or the NZB's file name
	// without .nzb, which for a grab is Sonarr's cleaned release title.
	Name     string
	Category string
	Priority int
	// Size is the download's size in bytes: the sum of the NZB's segments.
	Size int64
	// Progress is how much has downloaded, 0 to 1.
	Progress float64
	Status   Status
	// Storage is the finished job's folder as the container sees it,
	// CompleteDir/Name; HostPath is the same folder here.
	Storage  string
	HostPath string
	// FailMessage is why a failed job failed.
	FailMessage string
	// NZB is the NZB as uploaded, and NZBName its file name; for addurl,
	// URL is where it was to come from.
	NZB     []byte
	NZBName string
	URL     string
	// Added and Finished are when the job arrived and when it completed or
	// failed.
	Added    time.Time
	Finished time.Time
	// Archived is a history entry deleted with archive=1, which SABnzbd 4
	// keeps out of the history it lists rather than deleting.
	Archived bool
}

// Call is one API call the client received.
type Call struct {
	Method string
	// Mode is the mode= parameter: version, get_config, queue, history,
	// addfile, addurl, retry.
	Mode string
	// Name is the name= parameter, the action within a mode (delete,
	// pause) or the URL of an addurl.
	Name string
	// Params is every parameter as sent, query and form, the API key
	// included.
	Params url.Values
	// File is the uploaded file's name, for addfile.
	File string
	Time time.Time
}

// Options configure a Server.
type Options struct {
	// Addr is where to listen, e.g. ":18082"; ":0" picks a free port. The
	// container reaches the host through host.docker.internal, so a live run
	// listens on every interface.
	Addr string
	// APIKey is the key every call but version must carry; empty accepts
	// any.
	APIKey string
	// Category is the category Sonarr files its downloads under; default
	// "tv", Sonarr's own default.
	Category string
	// CompleteDir is SABnzbd's completed-download folder as the container
	// sees it, e.g. /downloads/complete: what get_config reports, and where
	// each finished job's storage is. It must exist in the container, or
	// Sonarr raises a remote path mapping health check.
	CompleteDir string
	// HostCompleteDir is the same folder on this machine, where Complete
	// writes a finished job's files.
	HostCompleteDir string
	// Version is the SABnzbd version reported; default DefaultVersion.
	Version string
	// PublicHost is the host the container reaches this process by, for
	// URL and Host; default 127.0.0.1.
	PublicHost string
	// Speed is the download speed the queue reports, in bytes a second, from
	// which each job's time left is worked out; default 10 MiB/s.
	Speed int64
}

// Server is a running fake SABnzbd.
type Server struct {
	apiKey          string
	category        string
	completeDir     string
	hostCompleteDir string
	version         string
	publicHost      string
	speed           int64

	listener net.Listener
	srv      *http.Server

	mu     sync.Mutex
	jobs   []*Job
	calls  []Call
	paused bool
}

// New starts a client listening on opts.Addr.
func New(opts Options) (*Server, error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:0"
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("sabnzbd: listening on %s: %w", opts.Addr, err)
	}

	s := &Server{
		apiKey:          opts.APIKey,
		category:        cmp.Or(opts.Category, "tv"),
		completeDir:     path.Clean(cmp.Or(opts.CompleteDir, "/downloads/complete")),
		hostCompleteDir: opts.HostCompleteDir,
		version:         cmp.Or(opts.Version, DefaultVersion),
		publicHost:      cmp.Or(opts.PublicHost, "127.0.0.1"),
		speed:           cmp.Or(opts.Speed, 10<<20),
		listener:        ln,
	}
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()

	return s, nil
}

// Host is the host the container reaches the client by, for Sonarr's host
// setting.
func (s *Server) Host() string { return s.publicHost }

// Port is the port listened on, for Sonarr's port setting.
func (s *Server) Port() int {
	if addr, ok := s.listener.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}

	return 0
}

// URL is the client's address as the container reaches it.
func (s *Server) URL() string {
	return "http://" + net.JoinHostPort(s.publicHost, strconv.Itoa(s.Port()))
}

// LocalURL is the client's address from this process.
func (s *Server) LocalURL() string {
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(s.Port()))
}

// Addr is the address listened on.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Category is the category Sonarr should be configured with.
func (s *Server) Category() string { return s.category }

// Close stops the client at once; there is nothing to drain.
func (s *Server) Close() error { return s.srv.Close() }

// ErrNoJob is a hook called with an id the client does not hold.
var ErrNoJob = errors.New("sabnzbd: no such job")

// ErrWrongState is a hook that does not apply to the job as it stands, such
// as completing one already in the history.
var ErrWrongState = errors.New("sabnzbd: the job is not in a state for that")

// Jobs returns every job, the queue in order and then the history, archived
// entries included.
func (s *Server) Jobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.queue() {
		out = append(out, *j)
	}
	for _, j := range s.history(true) {
		out = append(out, *j)
	}

	return out
}

// Job returns the job with an id.
func (s *Server) Job(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if j := s.find(id); j != nil {
		return *j, true
	}

	return Job{}, false
}

// FindJob returns the first job whose name holds text, ignoring case; a test
// knows the release title it grabbed, not the id SABnzbd gave it.
func (s *Server) FindJob(text string) (Job, bool) {
	for _, j := range s.Jobs() {
		if strings.Contains(strings.ToLower(j.Name), strings.ToLower(text)) {
			return j, true
		}
	}

	return Job{}, false
}

// AddJob queues a download Sonarr never grabbed, the way one added in
// SABnzbd by hand appears to it.
func (s *Server) AddJob(name, category string) Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	j := s.add(name, category, PriorityDefault, DefaultSize)

	return *j
}

// SetSize changes a job's size.
func (s *Server) SetSize(id string, size int64) error {
	return s.update(id, func(j *Job) error {
		j.Size = size
		return nil
	})
}

// SetProgress marks a queued job downloading, fraction (0 to 1) of the way.
func (s *Server) SetProgress(id string, fraction float64) error {
	return s.update(id, func(j *Job) error {
		if !queued(j.Status) {
			return fmt.Errorf("%w: %s is %s", ErrWrongState, j.ID, j.Status)
		}
		j.Progress = min(max(fraction, 0), 1)
		j.Status = StatusDownloading

		return nil
	})
}

// Pause pauses a queued job.
func (s *Server) Pause(id string) error {
	return s.update(id, func(j *Job) error {
		if !queued(j.Status) {
			return fmt.Errorf("%w: %s is %s", ErrWrongState, j.ID, j.Status)
		}
		j.Status = StatusPaused

		return nil
	})
}

// Resume resumes a paused job, downloading again when it had started.
func (s *Server) Resume(id string) error {
	return s.update(id, func(j *Job) error {
		if !queued(j.Status) {
			return fmt.Errorf("%w: %s is %s", ErrWrongState, j.ID, j.Status)
		}
		j.Status = resumed(j)

		return nil
	})
}

// PauseAll pauses or resumes the whole queue, which Sonarr shows as every
// download paused but a forced one.
func (s *Server) PauseAll(paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.paused = paused
}

// Complete finishes a queued job with the files given, relative paths to
// their content, written under HostCompleteDir/<name>, and moves it to the
// history, where Sonarr finds it completed at CompleteDir/<name>.
func (s *Server) Complete(id string, files map[string][]byte) error {
	return s.CompleteWith(id, func(dir string) error {
		for _, rel := range slices.Sorted(maps.Keys(files)) {
			target, err := within(dir, rel)
			if err != nil {
				return err
			}
			if err := mkdirAll(filepath.Dir(target)); err != nil {
				return err
			}
			if err := writeFile(target, files[rel]); err != nil {
				return err
			}
		}

		return nil
	})
}

// CompleteWith finishes a queued job with whatever fill writes into dir, the
// job's folder here, for a test that generates its media rather than
// holding it in memory.
func (s *Server) CompleteWith(id string, fill func(dir string) error) error {
	if s.hostCompleteDir == "" {
		return errors.New("sabnzbd: HostCompleteDir is not set, so there is nowhere to write a finished job")
	}

	s.mu.Lock()
	j := s.find(id)
	switch {
	case j == nil:
		s.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNoJob, id)
	case !queued(j.Status):
		s.mu.Unlock()
		return fmt.Errorf("%w: %s is %s", ErrWrongState, j.ID, j.Status)
	}
	folder := folderName(j.Name)
	s.mu.Unlock()

	hostDir := filepath.Join(s.hostCompleteDir, folder)
	if err := mkdirAll(hostDir); err != nil {
		return err
	}
	if err := fill(hostDir); err != nil {
		return fmt.Errorf("sabnzbd: filling %s: %w", hostDir, err)
	}

	return s.update(id, func(j *Job) error {
		j.Status = StatusCompleted
		j.Progress = 1
		j.Storage = path.Join(s.completeDir, folder)
		j.HostPath = hostDir
		j.Finished = time.Now()

		return nil
	})
}

// Fail fails a queued job with a message, the way SABnzbd reports a download
// it could not complete; empty is SABnzbd's own "cannot be completed".
func (s *Server) Fail(id, message string) error {
	return s.update(id, func(j *Job) error {
		if !queued(j.Status) {
			return fmt.Errorf("%w: %s is %s", ErrWrongState, j.ID, j.Status)
		}
		j.Status = StatusFailed
		j.FailMessage = cmp.Or(message, defaultFailMessage)
		j.Finished = time.Now()

		return nil
	})
}

// Remove drops a job from the queue or the history without touching its
// files, the way a download deleted in SABnzbd by hand vanishes from under
// Sonarr.
func (s *Server) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(s.jobs)
	s.jobs = slices.DeleteFunc(s.jobs, func(j *Job) bool { return j.ID == id })
	if len(s.jobs) == n {
		return fmt.Errorf("%w: %s", ErrNoJob, id)
	}

	return nil
}

// Calls returns every API call received since the last ResetCalls, oldest
// first.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.calls)
}

// CallsFor returns the calls of one mode.
func (s *Server) CallsFor(mode string) []Call {
	var out []Call
	for _, c := range s.Calls() {
		if c.Mode == mode {
			out = append(out, c)
		}
	}

	return out
}

// ResetCalls forgets the calls received so far.
func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = nil
}

// add creates a queued job. The caller holds the lock.
func (s *Server) add(name, category string, priority int, size int64) *Job {
	j := &Job{
		ID:       newID(),
		Name:     name,
		Category: s.resolveCategory(category),
		Priority: priority,
		Size:     size,
		Status:   StatusQueued,
		Added:    time.Now(),
	}
	if priority == PriorityPaused {
		j.Status = StatusPaused
	}
	s.jobs = append(s.jobs, j)

	return j
}

// resolveCategory is the category a job is filed under: one SABnzbd knows,
// or its catch-all "*".
func (s *Server) resolveCategory(category string) string {
	if strings.EqualFold(category, s.category) {
		return s.category
	}

	return "*"
}

// update applies change to a job under the lock.
func (s *Server) update(id string, change func(*Job) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	j := s.find(id)
	if j == nil {
		return fmt.Errorf("%w: %s", ErrNoJob, id)
	}

	return change(j)
}

// find returns the job with an id. The caller holds the lock.
func (s *Server) find(id string) *Job {
	for _, j := range s.jobs {
		if j.ID == id {
			return j
		}
	}

	return nil
}

// queue is the jobs in the queue, in the order they were added. The caller
// holds the lock.
func (s *Server) queue() []*Job {
	var out []*Job
	for _, j := range s.jobs {
		if queued(j.Status) {
			out = append(out, j)
		}
	}

	return out
}

// history is the finished jobs, the most recently finished first; archived
// ones only when asked for. The caller holds the lock.
func (s *Server) history(archived bool) []*Job {
	var out []*Job
	for _, j := range s.jobs {
		if !queued(j.Status) && (archived || !j.Archived) {
			out = append(out, j)
		}
	}
	slices.SortStableFunc(out, func(a, b *Job) int { return b.Finished.Compare(a.Finished) })

	return out
}

// queued reports whether a status is one of the queue's.
func queued(status Status) bool {
	return status == StatusQueued || status == StatusDownloading || status == StatusPaused
}

// resumed is the state a paused job resumes in.
func resumed(j *Job) Status {
	if j.Progress > 0 {
		return StatusDownloading
	}

	return StatusQueued
}

// newID is an nzo_id. Random rather than counted, so a fake restarted
// against a Sonarr that still remembers the last run's downloads cannot hand
// a new job an old job's id.
func newID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}

	return "SABnzbd_nzo_" + string(b)
}

// folderName is the folder a job finishes in: its name, with anything that
// would leave the complete folder replaced, as SABnzbd sanitises it.
func folderName(name string) string {
	name = strings.NewReplacer("/", "_", `\`, "_").Replace(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return "job"
	}

	return name
}

// within joins a relative path to dir, refusing one that climbs out of it.
func within(dir, rel string) (string, error) {
	target := filepath.Join(dir, filepath.FromSlash(rel))
	if back, err := filepath.Rel(dir, target); err != nil || back == "." || strings.HasPrefix(back, "..") {
		return "", fmt.Errorf("sabnzbd: %q is not a file inside the job's folder", rel)
	}

	return target, nil
}

// mkdirAll and writeFile lay files out for another user: the container
// imports them as its own uid and moves or deletes them afterwards, and the
// modes asked of MkdirAll and WriteFile are filtered by the umask, so the
// mode is applied again with chmod, which is not.
func mkdirAll(dir string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container writes here as another user
		return err
	}

	return os.Chmod(dir, 0o777) //nolint:gosec // same
}

func writeFile(file string, data []byte) error {
	if err := os.WriteFile(file, data, 0o666); err != nil { //nolint:gosec // the container moves it as another user
		return err
	}

	return os.Chmod(file, 0o666) //nolint:gosec // same
}
