package tools

import (
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// A library of thousands of series, which no seeded Sonarr is: the list
// pages through all of it, a title is found among it, and an audit that
// reads every series' files and episodes reads them several at a time -
// each once, never more at once than it is meant to, and with nothing lost
// or counted twice on the way.
func TestALibraryOfThousands(t *testing.T) {
	t.Parallel()

	const count = 3000
	// five files run twice as long as their episodes, spread through the
	// library so that a series dropped anywhere in it shows
	long := map[int]bool{1: true, 500: true, 1499: true, 2222: true, count: true}

	series := make([]map[string]any, 0, count)
	for id := 1; id <= count; id++ {
		series = append(series, map[string]any{
			"id": id, "title": fmt.Sprintf("Show %04d", id), "year": 2000 + id%25, "tvdbId": 100000 + id, "path": fmt.Sprintf("/tv/Show %04d", id), "runtime": 45,
			"monitored": true, "qualityProfileId": 1, "statistics": map[string]any{"episodeFileCount": 1, "episodeCount": 1},
		})
	}

	f := newFakeServer(t)
	library(t, f, series...)
	f.answer(t, "GET /api/v3/qualityprofile", []any{map[string]any{"id": 1, "name": "HD-1080p"}})
	f.answer(t, "GET /api/v3/tag", []any{})

	// what Sonarr takes to answer is taken here too, so reads made one after
	// another and reads made side by side are told apart
	var inFlight, most, fileReads, episodeReads atomic.Int32
	slow := func(reads *atomic.Int32, answer func(id int) any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			now := inFlight.Add(1)
			defer inFlight.Add(-1)

			// the most at once so far, kept by whichever read saw more
			for {
				seen := most.Load()
				if now <= seen || most.CompareAndSwap(seen, now) {
					break
				}
			}
			reads.Add(1)
			time.Sleep(2 * time.Millisecond)
			id, _ := strconv.Atoi(r.URL.Query().Get("seriesId"))
			writeJSON(t, w, answer(id))
		}
	}
	f.mux.HandleFunc("GET /api/v3/episodefile", slow(&fileReads, func(id int) any {
		runTime := "00:45:10"
		if long[id] {
			runTime = "01:32:00"
		}
		return []map[string]any{{"id": id, "seriesId": id, "seasonNumber": 1, "path": fmt.Sprintf("/tv/Show %04d/S01E01.mkv", id), "mediaInfo": map[string]any{"runTime": runTime, "resolution": "1920x1080"}}}
	}))
	f.mux.HandleFunc("GET /api/v3/episode", slow(&episodeReads, func(id int) any {
		return []map[string]any{{"id": id, "seriesId": id, "seasonNumber": 1, "episodeNumber": 1, "episodeFileId": id, "runtime": 45, "hasFile": true}}
	}))
	cs := session(t, f, Options{})

	// the list, a page at a time to its end
	seen, offset, pages := 0, 0, 0
	for {
		page := mustCall(t, cs, "series_list", map[string]any{"limit": 1000, "offset": offset})
		if number(page["total"]) != count {
			t.Fatalf("series_list counts %d series, want %d", number(page["total"]), count)
		}
		seen += len(rowsOf(page["series"]))
		pages++
		if offset = number(page["next_offset"]); offset == 0 {
			break
		}
	}
	if seen != count || pages != 3 {
		t.Errorf("paging the list read %d series in %d pages, want %d in 3", seen, pages, count)
	}

	// one series among them, by its title and by its tvdb id
	for _, ref := range []string{"show 2999", "tvdb:102999"} {
		if got := mustCall(t, cs, "series_get", map[string]any{"series": ref}); number(got["id"]) != 2999 {
			t.Errorf("series_get %q = %v", ref, got["id"])
		}
	}

	started := time.Now()
	out := mustCall(t, cs, "audit_runtime", map[string]any{"limit": 10})
	took := time.Since(started)
	if number(out["scanned"]) != count || number(out["total_findings"]) != len(long) || len(rowsOf(out["findings"])) != len(long) {
		t.Errorf("audit_runtime over %d series scanned %d and found %d, want every series and the %d long files", count, number(out["scanned"]), number(out["total_findings"]), len(long))
	}
	if fileReads.Load() != count || episodeReads.Load() != count {
		t.Errorf("the audit read %d series' files and %d series' episodes, want each of %d once", fileReads.Load(), episodeReads.Load(), count)
	}
	if got := most.Load(); got < 2 || got > auditWorkers {
		t.Errorf("the audit made %d reads at once at most, want several and no more than %d", got, auditWorkers)
	}
	// one after another, six thousand reads of two milliseconds are twelve
	// seconds before anything else; side by side they are a fraction of it
	if took > 8*time.Second {
		t.Errorf("the audit took %s over %d series", took.Round(time.Millisecond), count)
	}
	t.Logf("audit_runtime over %d series: %s, %d reads at once at most", count, took.Round(time.Millisecond), most.Load())

	// asked again, a new call reads again: nothing is kept between calls that
	// could go stale
	mustCall(t, cs, "audit_runtime", map[string]any{"limit": 1})
	if fileReads.Load() != 2*count {
		t.Errorf("a second audit read %d series' files in all, want it to read them afresh", fileReads.Load())
	}
}
