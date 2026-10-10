package tools

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
)

// libraryOfOne is a canned Sonarr that keeps what it is sent: one series, its
// tags and seasons as last written, and the tags made. A read of the series
// takes a moment, as a real one does, so calls made at once all read before
// any of them writes.
type libraryOfOne struct {
	mu      sync.Mutex
	tags    []int
	seasons map[int]bool
	labels  []string
}

func (l *libraryOfOne) series() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()

	seasons := make([]map[string]any, 0, len(l.seasons))
	for _, n := range slices.Sorted(func(yield func(int) bool) {
		for n := range l.seasons {
			if !yield(n) {
				return
			}
		}
	}) {
		seasons = append(seasons, map[string]any{"seasonNumber": n, "monitored": l.seasons[n]})
	}

	return map[string]any{"id": 1, "title": "Firefly", "tvdbId": 78874, "monitored": true, "tags": slices.Clone(l.tags), "seasons": seasons}
}

func (l *libraryOfOne) serve(t *testing.T, f *fakeServer) {
	t.Helper()

	f.mux.HandleFunc("GET /api/v3/series", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, []any{l.series()}) })
	f.mux.HandleFunc("GET /api/v3/series/1", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		writeJSON(t, w, l.series())
	})
	f.mux.HandleFunc("PUT /api/v3/series/1", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tags    []int `json:"tags"`
			Seasons []struct {
				SeasonNumber int  `json:"seasonNumber"`
				Monitored    bool `json:"monitored"`
			} `json:"seasons"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		l.mu.Lock()
		l.tags = body.Tags
		for _, s := range body.Seasons {
			l.seasons[s.SeasonNumber] = s.Monitored
		}
		l.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		writeJSON(t, w, l.series())
	})
	f.mux.HandleFunc("GET /api/v3/tag", func(w http.ResponseWriter, _ *http.Request) {
		l.mu.Lock()
		out := make([]map[string]any, 0, len(l.labels))
		for i, label := range l.labels {
			out = append(out, map[string]any{"id": i + 1, "label": label})
		}
		l.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		writeJSON(t, w, out)
	})
	f.mux.HandleFunc("POST /api/v3/tag", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Label string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		l.mu.Lock()
		l.labels = append(l.labels, body.Label)
		id := len(l.labels)
		l.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		writeJSON(t, w, map[string]any{"id": id, "label": body.Label})
	})
	f.answer(t, "GET /api/v3/qualityprofile", []any{})
}

// A turn's calls run at once, and an edit of a series sends the whole record
// back: eight calls each adding a tag to one series must leave it with eight
// tags, where each sending the tags as it found them would leave one. The
// same tag asked for by two of them is made once, and a season's flag changed
// beside them is not put back by an edit that read the series before it.
func TestEditsOfOneSeriesTakeTurns(t *testing.T) {
	t.Parallel()

	lib := &libraryOfOne{seasons: map[int]bool{1: true, 2: true}}
	f := newFakeServer(t)
	lib.serve(t, f)
	cs := session(t, f, Options{})

	const edits = 8
	var wg sync.WaitGroup
	failed := make(chan string, edits+1)
	for i := range edits {
		wg.Go(func() {
			if _, errText := callTool(t, cs, "series_edit", map[string]any{"series": "Firefly", "add_tags": []string{"tag" + strconv.Itoa(i), "shared"}}); errText != "" {
				failed <- errText
			}
		})
	}
	wg.Go(func() {
		if _, errText := callTool(t, cs, "season_monitor", map[string]any{"series": "Firefly", "seasons": []int{2}, "monitored": false}); errText != "" {
			failed <- errText
		}
	})
	wg.Wait()
	close(failed)
	for errText := range failed {
		t.Error(errText)
	}

	lib.mu.Lock()
	defer lib.mu.Unlock()

	if len(lib.tags) != edits+1 {
		t.Errorf("the series ended with tags %v, want the %d each call added and the one they share: an edit sent back the tags as they were before another's", lib.tags, edits)
	}
	if want := edits + 1; len(lib.labels) != want || len(slices.Compact(slices.Sorted(slices.Values(lib.labels)))) != want {
		t.Errorf("the tags made are %v, want %d with none made twice", lib.labels, want)
	}
	if lib.seasons[2] {
		t.Errorf("season 2 is monitored again after %d edits beside the call that turned it off", edits)
	}
}
