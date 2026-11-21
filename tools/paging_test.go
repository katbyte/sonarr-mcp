package tools

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A page is at most the limit asked for from the offset asked for, says how
// many rows there are in all, and names where the next page starts only when
// there is one. A limit of none is the default, and one past the cap is the
// cap.
func TestPageRows(t *testing.T) {
	t.Parallel()

	all := make([]int, 250)
	for i := range all {
		all[i] = i
	}

	for _, tt := range []struct {
		name        string
		in          pageIn
		first, rows int
		next        int
	}{
		{"no limit is the default", pageIn{}, 0, defaultLimit, defaultLimit},
		{"a page from the middle", pageIn{Limit: 50, Offset: 100}, 100, 50, 150},
		{"the last page names no next", pageIn{Limit: 100, Offset: 200}, 200, 50, 0},
		{"a page that ends exactly at the end names no next", pageIn{Limit: 50, Offset: 200}, 200, 50, 0},
		{"past the end is no rows and no next", pageIn{Limit: 10, Offset: 400}, 0, 0, 0},
		{"before the start is the start", pageIn{Limit: 10, Offset: -5}, 0, 10, 10},
		{"a limit below one is the default", pageIn{Limit: -1}, 0, defaultLimit, defaultLimit},
	} {
		rows, page := pageRows(all, tt.in)
		if len(rows) != tt.rows || (tt.rows > 0 && rows[0] != tt.first) || page.Total != len(all) || page.NextOffset != tt.next {
			t.Errorf("%s: %d rows from %v, total %d, next %d; want %d from %d, total %d, next %d", tt.name, len(rows), rows[:min(1, len(rows))], page.Total, page.NextOffset, tt.rows, tt.first, len(all), tt.next)
		}
	}

	if limit, _ := (pageIn{Limit: 1 << 30}).args(); limit != maxLimit {
		t.Errorf("a limit of a billion = %d, want the cap of %d", limit, maxLimit)
	}
	if rows, page := pageRows([]int(nil), pageIn{}); len(rows) != 0 || page.Total != 0 || page.NextOffset != 0 {
		t.Errorf("an empty list = %v, %+v", rows, page)
	}
}

// A listing Sonarr pages by page number is read from the offset asked for,
// not from the start of the page that offset falls in: an offset that is not
// a whole number of pages reads two of them and cuts to the rows wanted.
func TestWindow(t *testing.T) {
	t.Parallel()

	const total = 25
	var asked []string
	fetch := func(page, size int) ([]int, int, error) {
		asked = append(asked, strconv.Itoa(page)+"x"+strconv.Itoa(size))
		var rows []int
		for i := (page - 1) * size; i < min(page*size, total); i++ {
			rows = append(rows, i)
		}

		return rows, total, nil
	}

	for _, tt := range []struct {
		name  string
		in    pageIn
		want  []int
		next  int
		pages []string
	}{
		{"a whole page", pageIn{Limit: 10, Offset: 10}, []int{10, 11, 12, 13, 14, 15, 16, 17, 18, 19}, 20, []string{"2x10"}},
		{"an offset inside a page", pageIn{Limit: 10, Offset: 7}, []int{7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, 17, []string{"1x10", "2x10"}},
		{"the last, short page", pageIn{Limit: 10, Offset: 20}, []int{20, 21, 22, 23, 24}, 0, []string{"3x10"}},
		{"an offset inside the last page reads no page after it", pageIn{Limit: 10, Offset: 22}, []int{22, 23, 24}, 0, []string{"3x10"}},
		{"past the end", pageIn{Limit: 10, Offset: 40}, nil, 0, []string{"5x10"}},
	} {
		asked = nil
		rows, page, err := window(tt.in, fetch)
		if err != nil || !slices.Equal(rows, tt.want) || page.Total != total || page.NextOffset != tt.next || page.Offset != tt.in.Offset || !slices.Equal(asked, tt.pages) {
			t.Errorf("%s: rows %v, %+v, pages %v, %v; want rows %v, next %d, pages %v", tt.name, rows, page, asked, err, tt.want, tt.next, tt.pages)
		}
	}

	if _, _, err := window(pageIn{}, func(int, int) ([]int, int, error) { return nil, 0, errors.New("down") }); err == nil {
		t.Error("a listing that could not be read answered a page")
	}
}

// Every list is paged the one way, so a client that has paged one can page
// them all: it takes limit and offset, and answers total, offset and
// next_offset beside its rows. And every tool that deletes takes confirm and
// says what it would delete without it.
func TestEveryListPagesAndEveryDeleteConfirms(t *testing.T) {
	t.Parallel()

	listed, err := session(t, newFakeServer(t), Options{EnableDelete: true}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	properties := func(schema any) map[string]bool {
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for name := range s.Properties {
			out[name] = true
		}

		return out
	}

	kindOf := kinds(t)
	lists, deletes := 0, 0
	for _, tool := range listed.Tools {
		in, out := properties(tool.InputSchema), properties(tool.OutputSchema)
		if strings.HasSuffix(tool.Name, "_list") {
			lists++
			for _, name := range []string{"limit", "offset"} {
				if !in[name] {
					t.Errorf("%s takes no %s", tool.Name, name)
				}
			}
			for _, name := range []string{"total", "offset", "next_offset"} {
				if !out[name] {
					t.Errorf("%s answers no %s", tool.Name, name)
				}
			}
		}
		if kindOf[tool.Name] == deleteTool.String() {
			deletes++
			if !in["confirm"] || !out["would_delete"] || !out["deleted"] {
				t.Errorf("%s deletes: it takes confirm %v, and answers would_delete %v and deleted %v", tool.Name, in["confirm"], out["would_delete"], out["deleted"])
			}
		}
	}
	if lists != 30 || deletes != 15 {
		t.Errorf("held %d lists and %d deletes to the rule, want 30 and 15", lists, deletes)
	}
}

// A delete changes nothing until it is confirmed, and says what a confirmed
// call would remove: the series and, with its files asked for too, the folder
// and how much is in it.
func TestDeletesWaitForConfirm(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	firefly := map[string]any{
		"id": 1, "title": "Firefly", "year": 2002, "tvdbId": 78874, "path": "/tv/Firefly",
		"statistics": map[string]any{"episodeFileCount": 6, "sizeOnDisk": 3 << 30},
	}
	f.answer(t, "GET /api/v3/series", []any{firefly})
	f.answer(t, "GET /api/v3/series/1", firefly)
	f.answer(t, "GET /api/v3/episodefile/7", map[string]any{"id": 7, "path": "/tv/Firefly/Season 1/Firefly - S01E01.mkv"})
	f.answer(t, "GET /api/v3/episode", []any{})
	f.mux.HandleFunc("DELETE /api/v3/series/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	f.mux.HandleFunc("DELETE /api/v3/episodefile/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cs := session(t, f, Options{EnableDelete: true})

	would := mustCall(t, cs, "series_delete", map[string]any{"series": "Firefly", "delete_files": true})
	if text(would["would_delete"]) != "Firefly (2002)" || text(would["deleted"]) != "" || flag(would["files_deleted"]) {
		t.Errorf("without confirm = %v", would)
	}
	if files := text(would["files"]); !strings.Contains(files, "/tv/Firefly") || !strings.Contains(files, "6 episode files") {
		t.Errorf("what would go from disk = %q", files)
	}
	if gone := mustCall(t, cs, "file_delete", map[string]any{"file_ids": []any{7}}); !slices.Equal(strs(gone["would_delete"]), []string{"/tv/Firefly/Season 1/Firefly - S01E01.mkv"}) || len(strs(gone["deleted"])) != 0 {
		t.Errorf("file_delete without confirm = %v", gone)
	}
	if sent := append(f.sent(http.MethodDelete, "/api/v3/series/1"), f.sent(http.MethodDelete, "/api/v3/episodefile/7")...); len(sent) != 0 {
		t.Fatalf("%d deletes were sent without confirm", len(sent))
	}

	done := mustCall(t, cs, "series_delete", map[string]any{"series": "Firefly", "delete_files": true, "confirm": true})
	if text(done["deleted"]) != "Firefly (2002)" || text(done["would_delete"]) != "" || !flag(done["files_deleted"]) {
		t.Errorf("with confirm = %v", done)
	}
	deletes := slices.DeleteFunc(f.requests("/api/v3/series/1"), func(r request) bool { return r.Method != http.MethodDelete })
	if len(deletes) != 1 || !strings.Contains(deletes[0].Query, "deleteFiles=true") {
		t.Errorf("the deletes sent = %+v, want one, with the files", deletes)
	}
}

// A list answers the page asked for and where the next one starts, through
// the tool as a client calls it.
func TestListsPageThroughTheTools(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	series := make([]any, 0, 5)
	for i, title := range []string{"Andor", "Chernobyl", "Firefly", "Severance", "The Expanse"} {
		series = append(series, map[string]any{"id": i + 1, "title": title, "sortTitle": strings.ToLower(title), "tvdbId": 100 + i})
	}
	f.answer(t, "GET /api/v3/series", series)
	f.answer(t, "GET /api/v3/qualityprofile", []any{})
	f.answer(t, "GET /api/v3/tag", []any{})
	cs := session(t, f, Options{})

	first := mustCall(t, cs, "series_list", map[string]any{"limit": 2})
	if number(first["total"]) != 5 || number(first["offset"]) != 0 || number(first["next_offset"]) != 2 || len(rowsOf(first["series"])) != 2 {
		t.Errorf("the first page = %v", first)
	}
	last := mustCall(t, cs, "series_list", map[string]any{"limit": 2, "offset": 4})
	if _, more := last["next_offset"]; more || number(last["total"]) != 5 || len(rowsOf(last["series"])) != 1 || text(rowsOf(last["series"])[0]["title"]) != "The Expanse" {
		t.Errorf("the last page = %v", last)
	}
}

// The history is paged by Sonarr when no age is asked for, from the offset asked for even when that falls inside one of its pages. With an age it
// is everything since that day in one call, put newest first, and the count is of the entries inside it; one series' is still read newest first
// as far as the first entry older than that, since Sonarr cannot be asked for a series' entries by age.
func TestHistoryListPages(t *testing.T) {
	t.Parallel()

	// six entries, newest first: three from today, three from ten days ago
	all := make([]map[string]any, 0, 6)
	for i := range 6 {
		age := time.Duration(i+1) * time.Hour
		if i >= 3 {
			age += 10 * 24 * time.Hour
		}
		all = append(all, map[string]any{"id": 60 - i, "date": ago(age), "eventType": "grabbed", "sourceTitle": "release " + strconv.Itoa(i)})
	}
	f := newFakeServer(t)
	f.mux.HandleFunc("GET /api/v3/history", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
		from := min((n-1)*size, len(all))
		writeJSON(t, w, map[string]any{"page": n, "pageSize": size, "totalRecords": len(all), "records": all[from:min(from+size, len(all))]})
	})

	// everything since a date, oldest first and with an import among the grabs
	f.mux.HandleFunc("GET /api/v3/history/since", func(w http.ResponseWriter, r *http.Request) {
		since, err := time.Parse(time.RFC3339, r.URL.Query().Get("date"))
		if err != nil {
			t.Errorf("history since %q: %v", r.URL.Query().Get("date"), err)
		}

		recent := []map[string]any{}
		for _, h := range slices.Backward(all) {
			if at, _ := time.Parse(time.RFC3339, text(h["date"])); at.After(since) {
				recent = append(recent, h)
			}
		}
		writeJSON(t, w, append(recent, map[string]any{"id": 61, "date": ago(time.Minute), "eventType": "downloadFolderImported", "sourceTitle": "the import"}))
	})
	library(t, f, map[string]any{"id": 1, "title": "Firefly", "year": 2002, "tvdbId": 78874})
	cs := session(t, f, Options{})

	titles := func(out map[string]any) []string {
		entries := rowsOf(out["entries"])
		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, text(e["source_title"]))
		}

		return got
	}

	// no age: an offset inside Sonarr's page reads on into the next
	page := mustCall(t, cs, "history_list", map[string]any{"limit": 4, "offset": 3})
	if _, more := page["next_offset"]; more || number(page["total"]) != 6 || !slices.Equal(titles(page), []string{"release 3", "release 4", "release 5"}) {
		t.Errorf("from 3 of 6 = %v, %v", titles(page), page)
	}

	// an age: only what is inside it is counted and paged, newest first, and Sonarr is asked once rather than a page at a time
	paged := len(f.requests("/api/v3/history"))
	recent := mustCall(t, cs, "history_list", map[string]any{"days": 5, "limit": 2})
	if number(recent["total"]) != 4 || number(recent["next_offset"]) != 2 || !slices.Equal(titles(recent), []string{"the import", "release 0"}) {
		t.Errorf("the last five days = %v, %v", titles(recent), recent)
	}
	rest := mustCall(t, cs, "history_list", map[string]any{"days": 5, "limit": 2, "offset": 2})
	if _, more := rest["next_offset"]; more || number(rest["total"]) != 4 || !slices.Equal(titles(rest), []string{"release 1", "release 2"}) {
		t.Errorf("the rest of the last five days = %v, %v", titles(rest), rest)
	}
	if asked := len(f.requests("/api/v3/history")) - paged; asked != 0 || len(f.requests("/api/v3/history/since")) != 2 {
		t.Errorf("an age read %d pages of history and asked for what is since a date %d times, want no pages and one call for each", asked, len(f.requests("/api/v3/history/since")))
	}

	// an age and an event: the other events since then are left out
	grabs := mustCall(t, cs, "history_list", map[string]any{"days": 5, "events": []string{"grabbed"}})
	if number(grabs["total"]) != 3 || !slices.Equal(titles(grabs), []string{"release 0", "release 1", "release 2"}) {
		t.Errorf("the grabs of the last five days = %v, %v", titles(grabs), grabs)
	}

	// an age and a series: read back through that series' entries as far as the first older one
	since := len(f.requests("/api/v3/history/since"))
	one := mustCall(t, cs, "history_list", map[string]any{"series": "Firefly", "days": 5})
	if number(one["total"]) != 3 || !slices.Equal(titles(one), []string{"release 0", "release 1", "release 2"}) {
		t.Errorf("one series' last five days = %v, %v", titles(one), one)
	}
	if len(f.requests("/api/v3/history/since")) != since || !strings.Contains(f.requests("/api/v3/history")[len(f.requests("/api/v3/history"))-1].Query, "seriesIds=1") {
		t.Errorf("one series' history was not asked for by its series: %v", f.requests("/api/v3/history"))
	}
}

// A filter that names nothing Sonarr has is refused with what there is, before the library is read: a mistyped status, tag or profile answered
// with no series reads as there being none.
func TestAFilterThatNamesNothingIsRefused(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{"id": 1, "title": "Firefly", "status": "ended", "qualityProfileId": 1, "tags": []int{2}})
	f.answer(t, "GET /api/v3/qualityprofile", []any{map[string]any{"id": 1, "name": "HD-1080p"}, map[string]any{"id": 2, "name": "Any"}})
	f.answer(t, "GET /api/v3/tag", []any{map[string]any{"id": 2, "label": "scifi"}, map[string]any{"id": 3, "label": "anime"}})
	cs := session(t, f, Options{})

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"status": "finished"}, `status must be continuing, ended, upcoming or deleted, got "finished"`},
		{map[string]any{"tag": "sci-fi"}, `no tag "sci-fi" (have: anime, scifi)`},
		{map[string]any{"quality_profile": "HD"}, `no quality profile "HD" (have: Any, HD-1080p)`},
	} {
		if msg := mustFail(t, cs, "series_list", c.args); !strings.Contains(msg, c.want) {
			t.Errorf("series_list %v = %q, want %q", c.args, msg, c.want)
		}
	}
	if n := len(f.requests("/api/v3/series")); n != 0 {
		t.Errorf("the library was read %d times for filters that were refused", n)
	}

	// a name there is, in any case, filters; one nothing is on answers no series, which is then true
	for _, args := range []map[string]any{{"status": "Ended"}, {"tag": "SciFi"}, {"quality_profile": "hd-1080p"}} {
		if out := mustCall(t, cs, "series_list", args); number(out["total"]) != 1 {
			t.Errorf("series_list %v = %v, want Firefly", args, out)
		}
	}
	for _, args := range []map[string]any{{"status": "upcoming"}, {"tag": "anime"}, {"quality_profile": "Any"}} {
		if out := mustCall(t, cs, "series_list", args); number(out["total"]) != 0 {
			t.Errorf("series_list %v = %v, want no series", args, out)
		}
	}
}

// A list asked for again is cut from the read just made, a write through this server has the next one read again, and a list that only names
// series reads their titles from what is held, reading again only for a series it does not know.
func TestListsDoNotEachReadTheLibrary(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{"id": 1, "title": "Firefly", "qualityProfileId": 1}, map[string]any{"id": 2, "title": "Severance", "qualityProfileId": 1})
	f.answer(t, "GET /api/v3/qualityprofile", []any{map[string]any{"id": 1, "name": "HD-1080p"}})
	f.answer(t, "GET /api/v3/tag", []any{map[string]any{"id": 2, "label": "scifi"}})
	f.answer(t, "GET /api/v3/system/status", map[string]any{"version": "4.0.0"})
	f.answer(t, "GET /api/v3/health", []any{})
	f.answer(t, "GET /api/v3/tag/detail", []any{map[string]any{"id": 2, "label": "scifi", "seriesIds": []int{1, 2}}})
	f.answerStatus(t, "POST /api/v3/tag", http.StatusCreated, map[string]any{"id": 3, "label": "new"})

	// a release of a series the library has, and once gone is set one of a series it has not
	var gone atomic.Bool
	f.mux.HandleFunc("GET /api/v3/blocklist", func(w http.ResponseWriter, _ *http.Request) {
		blocked := []map[string]any{{"id": 7, "seriesId": 2, "sourceTitle": "Severance.S01E01", "date": ago(time.Hour)}, {"id": 8, "seriesId": 9, "sourceTitle": "Gone.S01E01", "date": ago(2 * time.Hour)}}
		if !gone.Load() {
			blocked = blocked[:1]
		}
		writeJSON(t, w, map[string]any{"page": 1, "pageSize": len(blocked), "totalRecords": len(blocked), "records": blocked})
	})
	cs := session(t, f, Options{})
	reads := func() int { return len(f.requests("/api/v3/series")) }

	mustCall(t, cs, "series_list", map[string]any{"limit": 1})
	mustCall(t, cs, "series_list", map[string]any{"limit": 1, "offset": 1})
	mustCall(t, cs, "series_list", map[string]any{"query": "fire"})
	if out := mustCall(t, cs, "profile_list", nil); number(rowsOf(out["profiles"])[0]["series"]) != 2 {
		t.Errorf("profile_list = %v, want 2 series on the profile", out)
	}
	if out := mustCall(t, cs, "server_info", nil); number(out["series"]) != 2 {
		t.Errorf("server_info = %v, want 2 series", out)
	}
	if out := mustCall(t, cs, "tag_list", nil); !slices.Equal(strs(rowsOf(out["tags"])[0]["series"]), []string{"Firefly", "Severance"}) {
		t.Errorf("tag_list = %v, want both series by title", out)
	}
	if out := mustCall(t, cs, "blocklist_list", nil); text(rowsOf(out["entries"])[0]["series"]) != "Severance" {
		t.Errorf("blocklist_list = %v, want its series by title", out)
	}
	if reads() != 1 {
		t.Errorf("seven lists read the library %d times, want once", reads())
	}

	// a write may have changed a series, so the list after it reads again
	mustCall(t, cs, "tag_create", map[string]any{"label": "new"})
	mustCall(t, cs, "series_list", nil)
	if reads() != 2 {
		t.Errorf("a list after a write read the library %d times in all, want it read again", reads())
	}

	// a series the read held does not know is looked for in a new one, once
	gone.Store(true)
	mustCall(t, cs, "blocklist_list", nil)
	if reads() != 3 {
		t.Errorf("a blocklist naming a series not held read the library %d times in all, want one more read", reads())
	}

	// an audit works from the library as it is now, whatever a list has just read
	mustCall(t, cs, "audit_monitoring", nil)
	if reads() != 4 {
		t.Errorf("an audit after a list read the library %d times in all, want it read afresh", reads())
	}
}

// A folder that is not there is refused, where Sonarr answers it as it does an empty one; an empty folder that is there is still answered empty.
func TestAFolderThatIsNotThereIsNotAnEmptyOne(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /api/v3/filesystem", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("path") {
		case "/data/":
			writeJSON(t, w, map[string]any{"parent": "/", "directories": []any{map[string]any{"name": "empty", "path": "/data/empty/", "type": "folder"}}, "files": []any{}})
		default:
			writeJSON(t, w, map[string]any{"parent": "/data/", "directories": []any{}, "files": []any{}})
		}
	})
	cs := session(t, f, Options{})

	if msg := mustFail(t, cs, "filesystem_list", map[string]any{"path": "/data/missing"}); !strings.Contains(msg, "no folder /data/missing as Sonarr sees it") || !strings.Contains(msg, "path /data/") {
		t.Errorf("a folder that is not there = %q", msg)
	}
	if out := mustCall(t, cs, "filesystem_list", map[string]any{"path": "/data/empty"}); number(out["total"]) != 0 || text(out["path"]) != "/data/empty" {
		t.Errorf("an empty folder = %v", out)
	}
	if out := mustCall(t, cs, "filesystem_list", map[string]any{"path": "/data"}); number(out["total"]) != 1 || len(f.requests("/api/v3/filesystem")) != 5 {
		t.Errorf("a folder with something in it = %v after %d calls, want it answered without a look at the folder above", out, len(f.requests("/api/v3/filesystem")))
	}
}

// A command a tool started is followed by its id, on its own, rather than
// looked for among the housekeeping Sonarr runs every minute.
func TestCommandListFollowsOneByID(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/command", []any{map[string]any{"id": 1, "name": "RefreshMonitoredDownloads", "status": "completed"}, map[string]any{"id": 7, "name": "CheckHealth", "status": "completed"}, map[string]any{"id": 42, "name": "SeriesSearch", "status": "started"}, map[string]any{"id": 3, "name": "RssSync", "status": "queued"}})
	f.answer(t, "GET /api/v3/command/42", map[string]any{"id": 42, "name": "SeriesSearch", "status": "started"})
	f.answerStatus(t, "GET /api/v3/command/43", http.StatusNotFound, map[string]any{"message": "NotFound"})
	cs := session(t, f, Options{})

	one := mustCall(t, cs, "command_list", map[string]any{"command_id": 42})
	if rows := rowsOf(one["commands"]); len(rows) != 1 || number(rows[0]["command_id"]) != 42 || text(rows[0]["status"]) != "started" || number(one["total"]) != 1 {
		t.Errorf("command 42 by its id = %v", one)
	}
	if msg := mustFail(t, cs, "command_list", map[string]any{"command_id": 43}); !strings.Contains(msg, "no command 43") {
		t.Errorf("an unknown command = %q", msg)
	}
	// Sonarr answers oldest first: what is running or waiting leads, then
	// what finished, the newest of it first
	all := rowsOf(mustCall(t, cs, "command_list", nil)["commands"])
	order := make([]int, 0, len(all))
	for _, c := range all {
		order = append(order, number(c["command_id"]))
	}
	if !slices.Equal(order, []int{42, 3, 7, 1}) {
		t.Errorf("the commands in order = %v, want the running and the queued, then the finished newest first", order)
	}
}

// An argument a tool does not take is refused, and the refusal names the
// ones it does: a caller that mistyped offset is shown limit and offset,
// rather than left to conclude the list cannot be paged.
func TestAnUnknownArgumentIsToldTheToolsOwn(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	cs := session(t, f, Options{})
	if msg := mustFail(t, cs, "tag_list", map[string]any{"ofset": 5}); !strings.Contains(msg, "ofset") || !strings.Contains(msg, "tag_list takes limit and offset") {
		t.Errorf("the refusal = %q", msg)
	}
	if len(f.requests("/api/v3/tag")) != 0 {
		t.Error("Sonarr was asked though the call was refused")
	}
}
