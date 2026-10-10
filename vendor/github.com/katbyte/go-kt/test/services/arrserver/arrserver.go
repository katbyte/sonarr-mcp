// Package arrserver is the servers a Prowlarr, Sonarr or Radarr calls home
// to, for a live suite to answer itself: the update server, the clock the
// application checks its own against, and the notices its makers post.
//
// The applications ask these whether or not anyone asked them to. The update
// server is asked whenever the health checks run once a build is two weeks
// old (UpdateCheck in the applications' source), and an answer that cannot be
// had ends the whole run of checks rather than the one, so a suite that cuts
// the application off from the internet has to answer it or lose every health
// check a fortnight after the release it pins. The clock is asked the same way
// (SystemTimeCheck), and an answer more than a day from the application's own
// is a health error, so a recording of it is wrong the day after it was made:
// this one answers with the time it is.
//
// It is an http.Handler for whatever serves the hosts: the applications reach
// them over HTTPS, so a suite puts it behind the proxy the container is
// pointed at (test/replayproxy, Proxy.Serve), for the whole host or, where
// the host's other paths are replayed from a recording, for the one path.
//
// It was written for prowlarr-mcp's suite.
package arrserver

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Update is one release the update server knows of.
type Update struct {
	// Version is the release, e.g. "2.7.0.5700".
	Version string
	// Released is when; zero is a day before the server started.
	Released time.Time
	// New and Fixed are its changes, as the release notes list them.
	New   []string
	Fixed []string
}

// Server is the servers, as one handler. The zero value is not usable; make
// one with New.
type Server struct {
	started time.Time

	mu       sync.Mutex
	updates  []Update
	down     bool
	requests []string
	unknown  []string
}

// New is a server that knows of no release, so no update is ever on offer.
func New() *Server { return &Server{started: time.Now().UTC()} }

// Offer sets the releases the update server knows of, replacing any it knew.
func (s *Server) Offer(updates ...Update) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.updates = slices.Clone(updates)
}

// SetDown makes every answer a 503, the way the services answer when they
// are not there, or puts them back.
func (s *Server) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.down = down
}

// Requests is every request received, in order, as a method and a path.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.requests)
}

// Unknown is the requests it had no answer for, which it answered 404: what
// an application asked that this does not know it asks.
func (s *Server) Unknown() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.unknown)
}

// Report says what a run should fail on at its end, "" for nothing: the
// requests there was no answer for, which are what an application has started
// asking that this does not know it asks. It reads as the replay proxy's
// report of its misses does.
func (s *Server) Report() string {
	unknown := s.Unknown()
	if len(unknown) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\narrserver: %d request(s) had no answer:\n", len(unknown))
	for _, u := range unknown {
		fmt.Fprintln(&b, "  "+u)
	}

	return b.String()
}

// updatePackage is a release as the update server describes it
// (UpdatePackage in the applications' source).
type updatePackage struct {
	Version     string        `json:"version"`
	ReleaseDate time.Time     `json:"releaseDate"`
	FileName    string        `json:"fileName"`
	URL         string        `json:"url"`
	Changes     updateChanges `json:"changes"`
	Hash        string        `json:"hash"`
	Branch      string        `json:"branch"`
}

type updateChanges struct {
	New   []string `json:"new"`
	Fixed []string `json:"fixed"`
}

// ServeHTTP answers the three things the applications ask, under /v1: the
// newest release above the one asking (update/<branch>), the recent releases
// (update/<branch>/changes), the time (time) and the notices (notification),
// of which there are none.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	down, updates := s.down, slices.Clone(s.updates)
	s.mu.Unlock()

	if down {
		http.Error(w, "the service is unavailable", http.StatusServiceUnavailable)

		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method != http.MethodGet || len(parts) < 2 || parts[0] != "v1":
	case len(parts) == 2 && parts[1] == "time":
		writeJSON(w, map[string]any{"dateTimeUtc": time.Now().UTC().Format(time.RFC3339)})

		return
	case len(parts) == 2 && parts[1] == "notification":
		writeJSON(w, []any{})

		return
	case len(parts) == 3 && parts[1] == "update":
		// newest first, and only one above the version that asks is an update
		packages := s.packages(updates, parts[2])
		if len(packages) > 0 && compareVersions(packages[0].Version, r.URL.Query().Get("version")) > 0 {
			writeJSON(w, map[string]any{"available": true, "updatePackage": packages[0]})

			return
		}
		writeJSON(w, map[string]any{"available": false})

		return
	case len(parts) == 4 && parts[1] == "update" && parts[3] == "changes":
		writeJSON(w, s.packages(updates, parts[2]))

		return
	}

	s.mu.Lock()
	s.unknown = append(s.unknown, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	http.NotFound(w, r)
}

// packages is the releases as the update server lists them, newest first.
func (s *Server) packages(updates []Update, branch string) []updatePackage {
	out := make([]updatePackage, 0, len(updates))
	for _, u := range updates {
		released := u.Released
		if released.IsZero() {
			released = s.started.AddDate(0, 0, -1)
		}
		out = append(out, updatePackage{Version: u.Version, ReleaseDate: released.UTC(), Branch: branch, FileName: "test." + branch + "." + u.Version + ".linux-musl-core-x64.tar.gz", URL: "https://updates.invalid/" + branch + "/" + u.Version + ".tar.gz", Changes: updateChanges{New: emptyNotNil(u.New), Fixed: emptyNotNil(u.Fixed)}})
	}
	slices.SortStableFunc(out, func(a, b updatePackage) int { return compareVersions(b.Version, a.Version) })

	return out
}

func emptyNotNil(s []string) []string {
	if s == nil {
		return []string{}
	}

	return s
}

// compareVersions orders two dotted versions by their parts, as numbers.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		var an, bn int
		if i < len(as) {
			an, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bn, _ = strconv.Atoi(bs[i])
		}
		if c := cmp.Compare(an, bn); c != 0 {
			return c
		}
	}

	return 0
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
