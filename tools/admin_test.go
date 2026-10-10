package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// What the live suite cannot confirm without taking Sonarr down under
// itself - a restore, a restart, a shutdown - each sends what it should once
// confirmed, and nothing before.
func TestRestoreRestartAndShutdownWaitForConfirm(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/system/backup", []any{map[string]any{"id": 3, "name": "sonarr_backup_2026.zip", "type": "manual", "size": 1 << 20, "time": "2026-10-01T10:00:00Z"}})
	for _, route := range []string{"POST /api/v3/system/backup/restore/3", "POST /api/v3/system/restart", "POST /api/v3/system/shutdown"} {
		f.mux.HandleFunc(route, func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, map[string]any{"restartRequired": true}) })
	}
	cs := session(t, f, Options{})

	for _, c := range []struct {
		tool, path, would, done string
		args                    map[string]any
	}{
		{"backup_restore", "/api/v3/system/backup/restore/3", "would_restore", "restored", map[string]any{"backup": "sonarr_backup_2026.zip"}},
		{"server_restart", "/api/v3/system/restart", "would", "done", map[string]any{}},
		{"server_shutdown", "/api/v3/system/shutdown", "would", "done", map[string]any{}},
	} {
		before := mustCall(t, cs, c.tool, c.args)
		if text(before[c.would]) == "" || text(before[c.done]) != "" || len(f.requests(c.path)) != 0 {
			t.Errorf("%s without confirm = %v, and %d calls were sent", c.tool, before, len(f.requests(c.path)))
		}
		c.args["confirm"] = true
		after := mustCall(t, cs, c.tool, c.args)
		if text(after[c.done]) == "" || text(after[c.would]) != "" || len(f.requests(c.path)) != 1 {
			t.Errorf("%s with confirm = %v, and %d calls were sent", c.tool, after, len(f.requests(c.path)))
		}
	}
	if msg := mustFail(t, cs, "backup_restore", map[string]any{"backup": "another.zip", "confirm": true}); !strings.Contains(msg, "no backup") {
		t.Errorf("a backup there is not = %q", msg)
	}
}

// Sonarr takes a command out of its queue and cannot stop one that runs: a
// command started, or finished, is refused before anything is sent, and one
// that starts between the look and the call is refused the same way.
func TestCommandCancel(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	for id, status := range map[string]string{"1": "queued", "2": "started", "3": "completed", "4": "queued"} {
		f.answer(t, "GET /api/v3/command/"+id, map[string]any{"id": json.Number(id), "name": "RescanSeries", "status": status})
	}
	f.mux.HandleFunc("DELETE /api/v3/command/1", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	f.answerStatus(t, "DELETE /api/v3/command/4", http.StatusConflict, map[string]any{"message": "Unable to cancel task"})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "command_cancel", map[string]any{"command_id": 1})
	if got := asObject(out["cancelled"]); number(got["command_id"]) != 1 || text(got["status"]) != "queued" || len(f.sent(http.MethodDelete, "/api/v3/command/1")) != 1 {
		t.Errorf("a queued command cancelled = %v", out)
	}
	for id, want := range map[int]string{2: "cannot stop a command once it is running", 3: "nothing left to cancel", 4: "cannot stop a command once it is running"} {
		if msg := mustFail(t, cs, "command_cancel", map[string]any{"command_id": id}); !strings.Contains(msg, want) {
			t.Errorf("cancelling command %d = %q, want %q in it", id, msg, want)
		}
	}
	if sent := len(f.requests("/api/v3/command/2")) + len(f.requests("/api/v3/command/3")); sent != 2 {
		t.Errorf("%d calls were made for the commands that could not be cancelled, want only the two looks", sent)
	}
}

// A search asked to queue and return at once sends its one command and reads
// the queue as it stands. The check of the download clients a search is
// followed by is a command too, and waits its turn behind the search: with
// it, the answer came only once Sonarr had a hand free.
func TestASearchOnlyQueuedReturnsAtOnce(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{"id": 1, "title": "Firefly", "year": 2002, "tvdbId": 78874, "seasons": []any{map[string]any{"seasonNumber": 1, "monitored": true}}})
	f.answer(t, "GET /api/v3/episode", []any{map[string]any{"id": 11, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 1, "title": "Serenity"}})
	f.answer(t, "GET /api/v3/qualityprofile", []any{})
	f.answer(t, "GET /api/v3/tag", []any{})
	f.answer(t, "GET /api/v3/queue/details", []any{})
	f.answer(t, "GET /api/v3/queue", page())
	f.answerStatus(t, "POST /api/v3/command", http.StatusCreated, map[string]any{"id": 9, "name": "EpisodeSearch", "status": "completed"})
	cs := session(t, f, Options{})

	searches := map[string]map[string]any{
		"episode_search": {"series": "Firefly", "episodes": []any{"S01E01"}},
		"season_search":  {"series": "Firefly", "season": 1},
		"series_search":  {"series": "Firefly"},
		"wanted_search":  {"kind": "missing"},
	}
	for name, args := range searches {
		before := len(f.sent(http.MethodPost, "/api/v3/command"))
		args["wait_seconds"] = -1
		mustCall(t, cs, name, args)

		if sent := f.sent(http.MethodPost, "/api/v3/command")[before:]; len(sent) != 1 || strings.Contains(sent[0], "RefreshMonitoredDownloads") {
			t.Errorf("%s only queued sent %v, want the search and nothing after it", name, sent)
		}
	}

	// waited for, the search is followed by the check, so what it grabbed is
	// in the queue it answers with
	for name, args := range searches {
		before := len(f.sent(http.MethodPost, "/api/v3/command"))
		delete(args, "wait_seconds")
		mustCall(t, cs, name, args)

		if sent := f.sent(http.MethodPost, "/api/v3/command")[before:]; len(sent) != 2 || !strings.Contains(sent[1], "RefreshMonitoredDownloads") {
			t.Errorf("%s waited for sent %v, want the search and then the check", name, sent)
		}
	}
}

// One change across many series is one call, and tags added and taken off
// are two, since Sonarr's editor does one or the other.
func TestSeriesBulkEdit(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{"id": 1, "title": "Firefly", "year": 2002, "tvdbId": 78874}, map[string]any{"id": 2, "title": "Chernobyl", "year": 2019, "tvdbId": 360893})
	f.answer(t, "GET /api/v3/tag", []any{map[string]any{"id": 5, "label": "old"}, map[string]any{"id": 6, "label": "new"}})
	f.answer(t, "GET /api/v3/qualityprofile", []any{map[string]any{"id": 4, "name": "HD-1080p"}})
	f.mux.HandleFunc("PUT /api/v3/series/editor", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(t, w, []any{map[string]any{"id": 1, "title": "Firefly"}, map[string]any{"id": 2, "title": "Chernobyl"}})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "series_bulk_edit", map[string]any{"series": []any{"Firefly", "tvdb:360893", "1"}, "quality_profile": "hd-1080p", "monitored": false, "add_tags": []any{"new"}, "remove_tags": []any{"old"}})
	sent := f.sent(http.MethodPut, "/api/v3/series/editor")
	if len(sent) != 2 || len(rowsOf(out["series"])) != 2 || len(strs(out["changed"])) != 4 {
		t.Fatalf("the bulk edit = %v, in %d calls", out, len(sent))
	}
	var first, second sonarr.SeriesEditorResource
	if err := json.Unmarshal([]byte(sent[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(sent[1]), &second); err != nil {
		t.Fatal(err)
	}
	if len(first.SeriesIds) != 2 || first.QualityProfileId != 4 || first.Monitored == nil || *first.Monitored || first.ApplyTags != sonarr.ApplyTagsAdd || len(first.Tags) != 1 || first.Tags[0] != 6 {
		t.Errorf("the first call = %+v, want the settings and the tag added on both series, named once each", first)
	}
	if second.ApplyTags != sonarr.ApplyTagsRemove || len(second.Tags) != 1 || second.Tags[0] != 5 || second.QualityProfileId != 0 || second.Monitored != nil {
		t.Errorf("the second call = %+v, want only the tag taken off", second)
	}
}

// The end of a log file, or only its lines with some text in them.
func TestLogFileRead(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.mux.HandleFunc("GET /api/v3/log/file/sonarr.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("1|Info|started\n2|Warn|Firefly.S01E01 not an upgrade\n3|Info|idle\n4|Error|FIREFLY.S01E02 import failed\n5|Info|done\n"))
	})
	f.mux.HandleFunc("GET /api/v3/log/file/gone.txt", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	cs := session(t, f, Options{})

	tail := mustCall(t, cs, "logfile_read", map[string]any{"lines": 2})
	if got := strs(tail["lines"]); len(got) != 2 || got[0] != "4|Error|FIREFLY.S01E02 import failed" || got[1] != "5|Info|done" || number(tail["matched"]) != 5 || text(tail["filename"]) != "sonarr.txt" {
		t.Errorf("the end of the file = %v", tail)
	}
	only := mustCall(t, cs, "logfile_read", map[string]any{"contains": "firefly"})
	if got := strs(only["lines"]); len(got) != 2 || !strings.Contains(got[0], "S01E01") || number(only["matched"]) != 2 {
		t.Errorf("the lines with the text = %v", only)
	}
	if msg := mustFail(t, cs, "logfile_read", map[string]any{"filename": "gone.txt"}); !strings.Contains(msg, "no log file") {
		t.Errorf("a file there is not = %q", msg)
	}
}
