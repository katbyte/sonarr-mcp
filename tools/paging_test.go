package tools

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
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

// The history is paged by Sonarr when no age is asked for, from the offset
// asked for even when that falls inside one of its pages; with an age it is
// read newest first as far as the first entry older than that, and the count
// is of the entries inside it.
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

	// an age: only what is inside it is counted and paged
	recent := mustCall(t, cs, "history_list", map[string]any{"days": 5, "limit": 2})
	if number(recent["total"]) != 3 || number(recent["next_offset"]) != 2 || !slices.Equal(titles(recent), []string{"release 0", "release 1"}) {
		t.Errorf("the last five days = %v, %v", titles(recent), recent)
	}
	rest := mustCall(t, cs, "history_list", map[string]any{"days": 5, "limit": 2, "offset": 2})
	if _, more := rest["next_offset"]; more || number(rest["total"]) != 3 || !slices.Equal(titles(rest), []string{"release 2"}) {
		t.Errorf("the rest of the last five days = %v, %v", titles(rest), rest)
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
