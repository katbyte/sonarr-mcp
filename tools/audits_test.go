package tools

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// ago is a time that far in the past, as Sonarr writes one.
func ago(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

// library answers the series list with the given series, and each series
// by its id.
func library(t *testing.T, f *fakeServer, series ...map[string]any) {
	t.Helper()

	if series == nil {
		series = []map[string]any{}
	}
	f.answer(t, "GET /api/v3/series", series)
	f.mux.HandleFunc("GET /api/v3/series/{id}", func(w http.ResponseWriter, r *http.Request) {
		for _, s := range series {
			if fmt.Sprint(s["id"]) == r.PathValue("id") {
				writeJSON(t, w, s)
				return
			}
		}
		http.NotFound(w, r)
	})
}

// page wraps records the way Sonarr's paged lists answer.
func page(records ...map[string]any) map[string]any {
	return map[string]any{"page": 1, "pageSize": len(records), "totalRecords": len(records), "records": records}
}

// problems lists each finding's problem, in order.
func problems(out map[string]any) []string {
	rows := rowsOf(out["findings"])
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, text(row["problem"]))
	}

	return got
}

// Only the downloads a person must deal with are findings: a finished one
// Sonarr cannot import, one waiting too long, an unreachable client, one
// that never started. A download going well, or one just queued, is not.
func TestAuditStuckDownloads(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly"})
	f.answer(t, "GET /api/v3/queue", page(
		map[string]any{
			"id": 1, "seriesId": 0, "title": "Some.Show.S01E01.1080p-GRP", "status": "completed",
			"trackedDownloadState": "importBlocked", "trackedDownloadStatus": "warning", "added": ago(2 * time.Hour),
			"statusMessages": []map[string]any{{"title": "Some.Show.S01E01", "messages": []string{"Unknown Series"}}},
		},
		map[string]any{
			"id": 2, "seriesId": 1, "title": "Firefly.S01E09", "status": "downloading", "trackedDownloadState": "downloading",
			"trackedDownloadStatus": "ok", "size": 1000, "sizeleft": 500, "added": ago(time.Hour),
		},
		map[string]any{
			"id": 3, "seriesId": 1, "title": "Firefly.S01E10", "status": "completed", "trackedDownloadState": "importPending",
			"trackedDownloadStatus": "ok", "added": ago(3 * time.Hour),
		},
		map[string]any{"id": 4, "seriesId": 1, "title": "Firefly.S01E11", "status": "downloadClientUnavailable", "added": ago(time.Hour)},
		map[string]any{
			"id": 5, "seriesId": 1, "title": "Firefly.S01E12", "status": "queued", "size": 1000, "sizeleft": 1000,
			"added": ago(48 * time.Hour),
		},
		map[string]any{
			"id": 6, "seriesId": 1, "title": "Firefly.S01E13", "status": "queued", "size": 1000, "sizeleft": 1000,
			"added": ago(time.Hour),
		},
	))
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_stuck_downloads", nil)
	want := []string{"cannot import", "waiting to import", "download client unreachable", "not starting"}
	if got := problems(out); !slices.Equal(got, want) {
		t.Errorf("problems = %v, want %v", got, want)
	}
	if number(out["scanned"]) != 6 || number(out["total_findings"]) != 4 {
		t.Errorf("scanned %v, found %v", out["scanned"], out["total_findings"])
	}
	// a download Sonarr cannot match to a series is still a finding in a
	// library of one series, and says Sonarr's reason
	blocked := rowsOf(out["findings"])[0]
	if !strings.Contains(text(blocked["detail"]), "Unknown Series") || !strings.Contains(text(blocked["fix"]), "import_apply") {
		t.Errorf("the blocked download = %v", blocked)
	}

	// asked about one series, the others' downloads are not looked at
	one := mustCall(t, cs, "audit_stuck_downloads", map[string]any{"series": "Firefly"})
	if number(one["scanned"]) != 5 || slices.Contains(problems(one), "cannot import") {
		t.Errorf("for Firefly alone = %v", one)
	}
}

// Failures are grouped by episode, most repeated first; a grab counts as
// lost only once it is old enough, and only when nothing was heard of its
// download again - no import, no failure, not in the queue.
func TestAuditFailedDownloads(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly"})
	series := map[string]any{"id": 1, "title": "Firefly"}
	episode := func(n int) map[string]any { return map[string]any{"seasonNumber": 1, "episodeNumber": n} }
	grab := func(id int, download string, when time.Duration) map[string]any {
		return map[string]any{
			"id": id, "seriesId": 1, "episodeId": 100 + id, "eventType": "grabbed", "downloadId": download, "date": ago(when),
			"sourceTitle": fmt.Sprintf("Firefly.S01E%02d.1080p-GRP", id), "series": series, "episode": episode(id),
			"data": map[string]string{"indexer": "Fake", "downloadClientName": "SAB"},
		}
	}
	f.answer(t, "GET /api/v3/history/since", []map[string]any{
		grab(1, "SAB_1", 10*time.Hour), // lost
		grab(2, "SAB_2", 10*time.Hour), // imported, below
		{"id": 20, "seriesId": 1, "episodeId": 102, "eventType": "downloadFolderImported", "downloadId": "SAB_2", "date": ago(9 * time.Hour)},
		grab(3, "SAB_3", time.Hour),    // too recent to call lost
		grab(4, "SAB_4", 10*time.Hour), // still in the queue
		{
			"id": 30, "seriesId": 1, "episodeId": 109, "eventType": "downloadFailed", "downloadId": "SAB_9a", "date": ago(48 * time.Hour),
			"sourceTitle": "Firefly.S01E09.720p-BAD", "series": series, "episode": episode(9), "data": map[string]string{"message": "CRC error"},
		},
		{
			"id": 31, "seriesId": 1, "episodeId": 109, "eventType": "downloadFailed", "downloadId": "SAB_9b", "date": ago(24 * time.Hour),
			"sourceTitle": "Firefly.S01E09.1080p-BAD", "series": series, "episode": episode(9), "data": map[string]string{"message": "Par2 repair failed"},
		},
		{
			"id": 32, "seriesId": 1, "episodeId": 110, "eventType": "downloadFailed", "downloadId": "SAB_10", "date": ago(24 * time.Hour),
			"sourceTitle": "Firefly.S01E10.1080p-ONE", "series": series, "episode": episode(10),
		},
	})
	// the queue's id is in another case from the history's
	f.answer(t, "GET /api/v3/queue", page(map[string]any{"id": 7, "seriesId": 1, "downloadId": "sab_4", "status": "downloading"}))
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_failed_downloads", nil)
	want := []string{"repeated failures", "download failed", "grab went nowhere"}
	if got := problems(out); !slices.Equal(got, want) {
		t.Fatalf("problems = %v, want %v\n%v", got, want, out)
	}
	rows := rowsOf(out["findings"])
	if text(rows[0]["subject"]) != "S01E09" || !strings.Contains(text(rows[0]["detail"]), "2 failed") ||
		!strings.Contains(text(rows[0]["detail"]), "last: Par2 repair failed") {
		t.Errorf("the repeated failure = %v", rows[0])
	}
	lost := rows[2]
	if !strings.Contains(text(lost["subject"]), "Firefly.S01E01") || !strings.Contains(text(lost["detail"]), "from Fake, sent to SAB") ||
		!strings.Contains(text(lost["fix"]), "history_mark_failed 1") {
		t.Errorf("the lost grab = %v", lost)
	}
}

// Health checks come errors first, and a root folder is short of space
// under a floor, or under a share of its disk when Sonarr knows the disk's
// size; one Sonarr cannot reach is reported as that instead.
func TestAuditHealth(t *testing.T) {
	t.Parallel()

	const gib = int64(1) << 30
	f := newFakeServer(t)
	library(t, f)
	f.answer(t, "GET /api/v3/health", []map[string]any{
		{"source": "UpdateCheck", "type": "notice", "message": "an update is available"},
		{"source": "IndexerStatusCheck", "type": "error", "message": "Indexers unavailable due to failures: Fake", "wikiUrl": "https://wiki.servarr.com/sonarr/system#indexers"},
	})
	f.answer(t, "GET /api/v3/rootfolder", []map[string]any{
		{"id": 1, "path": "/tv", "accessible": true, "freeSpace": 5 * gib},
		{"id": 2, "path": "/big/tv", "accessible": true, "freeSpace": 200 * gib},
		{"id": 3, "path": "/roomy", "accessible": true, "freeSpace": 500 * gib},
		{"id": 4, "path": "/gone", "accessible": false},
	})
	f.answer(t, "GET /api/v3/diskspace", []map[string]any{
		{"path": "/", "totalSpace": 1000 * gib},
		{"path": "/big", "totalSpace": 8192 * gib},
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_health", nil)
	rows := rowsOf(out["findings"])
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, text(row["subject"])+": "+text(row["problem"]))
	}
	want := []string{
		"IndexerStatusCheck: health error", "UpdateCheck: health notice",
		"/tv: root folder low on space", "/big/tv: root folder low on space", "/gone: root folder unreachable",
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	for _, row := range rowsOf(out["findings"]) {
		switch text(row["subject"]) {
		case "IndexerStatusCheck":
			if !strings.Contains(text(row["fix"]), "wiki.servarr.com") {
				t.Errorf("the error's fix = %v", row["fix"])
			}
		case "/big/tv":
			// the disk is the longest path holding the folder, not /
			if !strings.Contains(text(row["detail"]), "of 8.0 TiB (2%)") {
				t.Errorf("/big/tv = %v", row["detail"])
			}
		}
	}
}

// A title several series share is refused with every match listed, and the
// year, a provider id or Sonarr's id picks one.
func TestSeriesNamesThatFitSeveral(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f,
		map[string]any{"id": 1, "title": "The Office", "year": 2001, "tvdbId": 78107, "path": "/tv/The Office (UK)"},
		map[string]any{"id": 2, "title": "The Office", "year": 2005, "tvdbId": 73244, "path": "/tv/The Office (US)"},
		map[string]any{"id": 3, "title": "Firefly", "year": 2002, "tvdbId": 78874, "path": "/tv/Firefly"},
	)
	f.answer(t, "GET /api/v3/qualityprofile", []any{})
	f.answer(t, "GET /api/v3/tag", []any{})
	cs := session(t, f, Options{})

	msg := mustFail(t, cs, "series_get", map[string]any{"series": "the office"})
	for _, want := range []string{"matches 2 series", "The Office (2001)", "The Office (2005)", "add the year"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal %q lacks %q", msg, want)
		}
	}
	for ref, want := range map[string]int{"The Office (2005)": 2, "the office 2001": 1, "tvdb:73244": 2, "1": 1, "fire": 3} {
		if got := mustCall(t, cs, "series_get", map[string]any{"series": ref}); number(got["id"]) != want {
			t.Errorf("%q = series %v, want %d", ref, got["id"], want)
		}
	}
	if msg := mustFail(t, cs, "series_get", map[string]any{"series": "Serenity"}); !strings.Contains(msg, "no series in Sonarr matches") {
		t.Errorf("an unknown title = %q", msg)
	}
}

// With Rename Episodes off Sonarr's rename plan is always empty, so
// series_rename refuses rather than report nothing to do, and sends no
// command.
func TestSeriesRenameRefusesWithRenamingOff(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{"id": 3, "title": "Firefly", "year": 2002, "path": "/tv/Firefly"})
	f.answer(t, "GET /api/v3/config/naming", map[string]any{"id": 1, "renameEpisodes": false})
	cs := session(t, f, Options{})

	if msg := mustFail(t, cs, "series_rename", map[string]any{"series": "Firefly"}); !strings.Contains(msg, "Rename Episodes setting is off") {
		t.Errorf("the refusal = %q", msg)
	}
	if n := len(f.requests("/api/v3/rename")) + len(f.requests("/api/v3/command")); n != 0 {
		t.Errorf("%d requests for a plan or a command", n)
	}
}

// file_delete reads every file before deleting any, so one unknown id
// deletes nothing.
func TestFileDeleteChecksEveryFileFirst(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/episodefile/7", map[string]any{"id": 7, "path": "/tv/Firefly/Season 1/Firefly - S01E01.mkv"})
	f.answerStatus(t, "GET /api/v3/episodefile/8", http.StatusNotFound, map[string]any{"message": "NotFound"})
	f.answer(t, "GET /api/v3/episode", []any{})
	f.mux.HandleFunc("DELETE /api/v3/episodefile/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cs := session(t, f, Options{EnableDelete: true})

	if msg := mustFail(t, cs, "file_delete", map[string]any{"file_ids": []any{7, 8}, "confirm": true}); !strings.Contains(msg, "no episode file 8") {
		t.Errorf("the refusal = %q", msg)
	}
	if deleted := f.sent(http.MethodDelete, "/api/v3/episodefile/7"); len(deleted) != 0 {
		t.Error("file 7 was deleted though file 8 does not exist")
	}

	out := mustCall(t, cs, "file_delete", map[string]any{"file_ids": []any{7}, "confirm": true})
	if got := strs(out["deleted"]); !slices.Equal(got, []string{"/tv/Firefly/Season 1/Firefly - S01E01.mkv"}) {
		t.Errorf("deleted = %v", got)
	}
}

// A file can hold two episodes, and Sonarr downloads again what it still
// monitors: the answer names every episode a file takes with it, and warns of
// the ones that would come back, before anything is deleted and after. With
// Sonarr set to unmonitor what is deleted it says that instead.
func TestFileDeleteNamesEveryEpisodeAndWarns(t *testing.T) {
	t.Parallel()

	var unmonitors atomic.Bool
	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/episodefile/7", map[string]any{"id": 7, "seriesId": 1, "seasonNumber": 1, "size": 1 << 30, "path": "/tv/Firefly/Season 1/Firefly - S01E01-E02.mkv"})
	f.answer(t, "GET /api/v3/episodefile/9", map[string]any{"id": 9, "seriesId": 1, "seasonNumber": 1, "size": 1 << 29, "path": "/tv/Firefly/Season 1/Firefly - S01E03.mkv"})
	f.answer(t, "GET /api/v3/episode", []any{
		map[string]any{"id": 11, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 1, "episodeFileId": 7, "monitored": true},
		map[string]any{"id": 12, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 2, "episodeFileId": 7, "monitored": false},
		map[string]any{"id": 13, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 3, "episodeFileId": 9, "monitored": false},
		map[string]any{"id": 14, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 4, "monitored": true},
	})
	f.mux.HandleFunc("GET /api/v3/config/mediamanagement", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"id": 1, "autoUnmonitorPreviouslyDownloadedEpisodes": unmonitors.Load()})
	})
	f.mux.HandleFunc("DELETE /api/v3/episodefile/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cs := session(t, f, Options{EnableDelete: true})

	would := mustCall(t, cs, "file_delete", map[string]any{"file_ids": []any{7, 9}})
	files := rowsOf(would["files"])
	if len(files) != 2 || text(files[0]["episodes"]) != "S01E01-E02" || !flag(files[0]["monitored"]) || text(files[0]["size"]) == "" {
		t.Fatalf("the file of two episodes = %v", would["files"])
	}
	if text(files[1]["episodes"]) != "S01E03" || flag(files[1]["monitored"]) {
		t.Errorf("the file of an unmonitored episode = %v", files[1])
	}
	if w := text(would["warning"]); !strings.Contains(w, "S01E01") || strings.Contains(w, "S01E02") || strings.Contains(w, "S01E04") || !strings.Contains(w, "episode_monitor") {
		t.Errorf("the warning = %q, want the one monitored episode on these files and how to stop it coming back", w)
	}

	// nothing on the file is monitored: nothing to warn of, and the setting
	// is not asked for
	asked := len(f.requests("/api/v3/config/mediamanagement"))
	if quiet := mustCall(t, cs, "file_delete", map[string]any{"file_ids": []any{9}}); text(quiet["warning"]) != "" || len(f.requests("/api/v3/config/mediamanagement")) != asked {
		t.Errorf("a file of unmonitored episodes = %v", quiet)
	}

	done := mustCall(t, cs, "file_delete", map[string]any{"file_ids": []any{7}, "confirm": true})
	if !strings.Contains(text(done["warning"]), "S01E01") || len(strs(done["deleted"])) != 1 || len(rowsOf(done["files"])) != 1 {
		t.Errorf("the confirmed delete = %v", done)
	}

	// Sonarr set to unmonitor what is deleted changes the episode's flag
	// instead, which is said too
	unmonitors.Store(true)
	set := mustCall(t, cs, "file_delete", map[string]any{"file_ids": []any{7}})
	if w := text(set["warning"]); !strings.Contains(w, "stops monitoring S01E01") || strings.Contains(w, "downloads them again once") || !flag(rowsOf(set["files"])[0]["monitored"]) {
		t.Errorf("with Sonarr unmonitoring what is deleted = %v", set)
	}
}

// A season's flag is not a count of its episodes: episode_monitor leaves it
// be, so a season can say unmonitored with monitored episodes in it. Asked to
// unmonitor that season, the tool unmonitors those episodes rather than
// finding nothing to do, and sends the series only when a flag of its own
// changes.
func TestSeasonMonitorSetsEpisodesTheFlagMissed(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	firefly := map[string]any{
		"id": 1, "title": "Firefly", "year": 2002, "tvdbId": 78874, "monitored": true,
		"seasons": []any{map[string]any{"seasonNumber": 0, "monitored": false}, map[string]any{"seasonNumber": 1, "monitored": false}, map[string]any{"seasonNumber": 2, "monitored": true}},
	}
	f.answer(t, "GET /api/v3/series", []any{firefly})
	f.answer(t, "GET /api/v3/series/1", firefly)
	f.answer(t, "GET /api/v3/episode", []any{
		map[string]any{"id": 11, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 1, "monitored": true},
		map[string]any{"id": 12, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 2, "monitored": false},
		map[string]any{"id": 13, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 3, "monitored": true},
		map[string]any{"id": 21, "seriesId": 1, "seasonNumber": 2, "episodeNumber": 1, "monitored": true},
	})
	f.mux.HandleFunc("PUT /api/v3/episode/monitor", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(t, w, []any{})
	})
	f.mux.HandleFunc("PUT /api/v3/series/1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(t, w, firefly)
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "season_monitor", map[string]any{"series": "Firefly", "seasons": []any{1}, "monitored": false})
	if changed := strs(out["changed"]); len(changed) != 1 || !strings.Contains(changed[0], "season 1: 2 episodes monitored false") {
		t.Errorf("changed = %v, want the two episodes the season's flag missed", changed)
	}
	sent := f.sent(http.MethodPut, "/api/v3/episode/monitor")
	if len(sent) != 1 || !strings.Contains(sent[0], `"episodeIds":[11,13]`) || !strings.Contains(sent[0], `"monitored":false`) {
		t.Errorf("the episodes sent = %v, want 11 and 13 unmonitored", sent)
	}
	if series := f.sent(http.MethodPut, "/api/v3/series/1"); len(series) != 0 {
		t.Errorf("the series was sent though none of its flags changed: %v", series)
	}

	// a season with nothing left to change is still nothing to do
	if out := mustCall(t, cs, "season_monitor", map[string]any{"series": "Firefly", "seasons": []any{0}, "monitored": false}); len(strs(out["changed"])) != 0 || len(f.sent(http.MethodPut, "/api/v3/episode/monitor")) != 1 {
		t.Errorf("a season already as asked, episodes and all = %v", out)
	}

	// monitoring a season already monitored, with nothing in it to set, sends
	// nothing either
	if out := mustCall(t, cs, "season_monitor", map[string]any{"series": "Firefly", "seasons": []any{2}, "monitored": true}); len(strs(out["changed"])) != 0 || len(f.sent(http.MethodPut, "/api/v3/series/1")) != 0 {
		t.Errorf("a monitored season of a monitored series = %v", out)
	}

	// a season the series does not have is refused, naming the ones it has
	if msg := mustFail(t, cs, "season_monitor", map[string]any{"series": "Firefly", "seasons": []any{7}, "monitored": false}); !strings.Contains(msg, "no season 7") || !strings.Contains(msg, "0, 1, 2") {
		t.Errorf("the refusal = %q", msg)
	}
	if msg := mustFail(t, cs, "season_search", map[string]any{"series": "Firefly", "season": 7}); !strings.Contains(msg, "no season 7") || !strings.Contains(msg, "0, 1, 2") {
		t.Errorf("the search's refusal = %q", msg)
	}
}

// Monitoring a season of a series that is not monitored does nothing unless
// the series follows, whether or not the season's own flag had to change.
func TestSeasonMonitorTurnsTheSeriesOn(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	firefly := map[string]any{"id": 1, "title": "Firefly", "year": 2002, "tvdbId": 78874, "monitored": false, "seasons": []any{map[string]any{"seasonNumber": 1, "monitored": true}}}
	f.answer(t, "GET /api/v3/series", []any{firefly})
	f.answer(t, "GET /api/v3/series/1", firefly)
	f.answer(t, "GET /api/v3/episode", []any{map[string]any{"id": 11, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 1, "monitored": true}})
	f.mux.HandleFunc("PUT /api/v3/series/1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		writeJSON(t, w, firefly)
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "season_monitor", map[string]any{"series": "Firefly", "seasons": []any{1}, "monitored": true})
	if changed := strs(out["changed"]); !slices.Equal(changed, []string{"series: monitored true"}) {
		t.Errorf("changed = %v, want the series turned on", changed)
	}
	sent := f.sent(http.MethodPut, "/api/v3/series/1")
	if len(sent) != 1 {
		t.Fatalf("the series was sent %d times, want once", len(sent))
	}
	var body struct {
		Monitored bool `json:"monitored"`
	}
	if err := json.Unmarshal([]byte(sent[0]), &body); err != nil || !body.Monitored {
		t.Errorf("the series sent = %s (%v), want it monitored", sent[0], err)
	}
}

// Testing every indexer answers 400 when any fails, with every result in
// the body: the tool reports each, rather than the 400.
func TestIndexerTestReadsFailures(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	f.answer(t, "GET /api/v3/indexer", []map[string]any{{"id": 1, "name": "Good"}, {"id": 2, "name": "Bad"}})
	f.answerStatus(t, "POST /api/v3/indexer/testall", http.StatusBadRequest, []map[string]any{
		{"id": 1, "isValid": true, "validationFailures": []any{}},
		{"id": 2, "isValid": false, "validationFailures": []map[string]any{{"propertyName": "ApiKey", "errorMessage": "Invalid API Key"}}},
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "indexer_test", nil)
	results := rowsOf(out["results"])
	if len(results) != 2 || text(results[0]["name"]) != "Bad" || flag(results[0]["ok"]) || !flag(results[1]["ok"]) {
		t.Fatalf("results = %v", results)
	}
	if got := strs(results[0]["problems"]); !slices.Equal(got, []string{"ApiKey: Invalid API Key"}) {
		t.Errorf("Bad's problems = %v", got)
	}
}

// strs pulls a list of strings out of a decoded JSON field.
func strs(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		out = append(out, text(e))
	}

	return out
}

func TestPickLookup(t *testing.T) {
	t.Parallel()

	found := []sonarr.SeriesResource{
		{Title: "The Office", Year: 2001, TvdbId: 78107},
		{Title: "The Office (US)", Year: 2005, TvdbId: 73244},
		{Title: "The Office (2012)", Year: 2012, TvdbId: 264581},
		{Title: "Firefly", Year: 2002, TvdbId: 78874},
	}
	for _, c := range []struct {
		term string
		want int // tvdb id, 0 for none
	}{
		{"tvdb:73244", 73244},
		{"TVDB:78874", 78874},
		{"tvdb:1", 0},
		{"The Office", 0}, // three shows go by it
		{"The Office (US)", 73244},
		{"The Office (2001)", 78107},
		{"the office (2012)", 264581},
		{"Firefly", 78874},
		{"Firefly (2003)", 0}, // the wrong year
		{"Serenity", 0},
	} {
		got, ok := pickLookup(c.term, found)
		switch {
		case c.want == 0 && ok:
			t.Errorf("%q picked %s (%d)", c.term, got.Title, got.Year)
		case c.want != 0 && (!ok || got.TvdbId != sonarr.TvdbID(c.want)):
			t.Errorf("%q = %v, want tvdb %d", c.term, got, c.want)
		}
	}
}

func TestResolutionClass(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		frame string
		want  int
	}{
		{"3840x2160", 2160},
		{"1920x1080", 1080},
		{"1920x800", 1080},
		{"1440x1080", 1080},
		{"1280x720", 720},
		{"960x720", 720},
		{"1280x534", 720},
		{"720x576", 480},
		{"640x360", 480},
		{"1920X1080", 1080},
		{" 1280 x 720 ", 720},
		{"", 0},
		{"1080p", 0},
		{"0x0", 0},
		{"axb", 0},
	} {
		if got := resolutionClass(c.frame); got != c.want {
			t.Errorf("resolutionClass(%q) = %d, want %d", c.frame, got, c.want)
		}
	}
	for res, want := range map[int]int{0: 0, 360: 480, 480: 480, 576: 480, 720: 720, 1080: 1080} {
		if got := sdClass(res); got != want {
			t.Errorf("sdClass(%d) = %d, want %d", res, got, want)
		}
	}
}

func TestLowOnSpace(t *testing.T) {
	t.Parallel()

	const gib = int64(1) << 30
	for _, c := range []struct {
		free, total int64
		want        bool
	}{
		{5 * gib, 0, true},             // under the floor, disk unknown
		{20 * gib, 0, false},           // over it
		{20 * gib, 1024 * gib, true},   // over it, but 2% of the disk
		{100 * gib, 1024 * gib, false}, // 10%
	} {
		if got := lowOnSpace(c.free, c.total); got != c.want {
			t.Errorf("lowOnSpace(%d GiB, %d GiB) = %v", c.free/gib, c.total/gib, got)
		}
	}
}

func TestStuckProblem(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name string
		q    sonarr.QueueResource
		age  time.Duration
		want string
	}{
		{"downloading", sonarr.QueueResource{Status: sonarr.QueueStatusDownloading, TrackedDownloadStatus: sonarr.TrackedDownloadStatusOk}, time.Hour, ""},
		{"blocked", sonarr.QueueResource{TrackedDownloadState: sonarr.TrackedDownloadStateImportBlocked}, time.Minute, "cannot import"},
		{"pending with a warning", sonarr.QueueResource{TrackedDownloadState: sonarr.TrackedDownloadStateImportPending, TrackedDownloadStatus: sonarr.TrackedDownloadStatusWarning}, time.Minute, "cannot import"},
		{"pending a moment", sonarr.QueueResource{TrackedDownloadState: sonarr.TrackedDownloadStateImportPending, TrackedDownloadStatus: sonarr.TrackedDownloadStatusOk}, time.Minute, ""},
		{"pending for hours", sonarr.QueueResource{TrackedDownloadState: sonarr.TrackedDownloadStateImportPending, TrackedDownloadStatus: sonarr.TrackedDownloadStatusOk}, 2 * time.Hour, "waiting to import"},
		{"failed", sonarr.QueueResource{Status: sonarr.QueueStatusFailed}, time.Minute, "download failed"},
		{"failed pending", sonarr.QueueResource{TrackedDownloadState: sonarr.TrackedDownloadStateFailedPending}, time.Minute, "download failed"},
		{"paused", sonarr.QueueResource{Status: sonarr.QueueStatusPaused}, time.Minute, "paused in the download client"},
		{"error", sonarr.QueueResource{Status: sonarr.QueueStatusDownloading, TrackedDownloadStatus: sonarr.TrackedDownloadStatusError}, time.Minute, "error"},
		{"warning", sonarr.QueueResource{Status: sonarr.QueueStatusWarning}, time.Minute, "warning"},
		{"not starting", sonarr.QueueResource{Status: sonarr.QueueStatusQueued, Size: 10, Sizeleft: 10}, 25 * time.Hour, "not starting"},
		{"queued a while", sonarr.QueueResource{Status: sonarr.QueueStatusQueued, Size: 10, Sizeleft: 10}, 23 * time.Hour, ""},
		{"queued and started", sonarr.QueueResource{Status: sonarr.QueueStatusQueued, Size: 10, Sizeleft: 5}, 25 * time.Hour, ""},
		{"held by a delay profile", sonarr.QueueResource{Status: sonarr.QueueStatusDelay}, 25 * time.Hour, ""},
	} {
		if got, _ := stuckProblem(&c.q, c.age); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRoundAge(t *testing.T) {
	t.Parallel()

	for d, want := range map[time.Duration]string{
		30 * time.Minute: "30 minutes", 5 * time.Hour: "5 hours", 47 * time.Hour: "47 hours", 72 * time.Hour: "3 days",
	} {
		if got := roundAge(d); got != want {
			t.Errorf("roundAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestFirstLineAndCommandDone(t *testing.T) {
	t.Parallel()

	if got := firstLine("  boom\n  at Sonarr.Something()\n"); got != "boom" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine(" one line "); got != "one line" {
		t.Errorf("firstLine = %q", got)
	}
	for s, want := range map[sonarr.CommandStatus]bool{
		"": false, sonarr.CommandStatusQueued: false, sonarr.CommandStatusStarted: false,
		sonarr.CommandStatusCompleted: true, sonarr.CommandStatusFailed: true, sonarr.CommandStatusAborted: true,
	} {
		if got := commandDone(s); got != want {
			t.Errorf("commandDone(%q) = %v", s, got)
		}
	}
}

// dirs answers Sonarr's folder listing for a parent folder, and its video
// file listing for any folder in it.
func dirs(t *testing.T, f *fakeServer, parent string, folders map[string]int) {
	t.Helper()

	listing := make([]map[string]any, 0, len(folders))
	for name := range folders {
		listing = append(listing, map[string]any{"type": "folder", "name": name, "path": parent + "/" + name})
	}
	f.answer(t, "GET /api/v3/filesystem", map[string]any{"parent": "/", "directories": listing, "files": []any{}})
	f.mux.HandleFunc("GET /api/v3/filesystem/mediafiles", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		files := []map[string]any{}
		for name, n := range folders {
			if path != parent+"/"+name {
				continue
			}
			for i := range n {
				file := fmt.Sprintf("%s/S01E%02d.mkv", path, i+1)
				files = append(files, map[string]any{"path": file, "name": fmt.Sprintf("S01E%02d.mkv", i+1)})
			}
		}
		writeJSON(t, w, files)
	})
}

// rootFolder answers the root folder list with the folders Sonarr does not
// know as a series.
//
//nolint:unparam // the path is the library's, and named at each call for what it is
func rootFolder(t *testing.T, f *fakeServer, path string, accessible bool, unmapped ...string) {
	t.Helper()

	folders := make([]map[string]any, 0, len(unmapped))
	for _, name := range unmapped {
		folders = append(folders, map[string]any{"name": name, "path": path + "/" + name})
	}
	f.answer(t, "GET /api/v3/rootfolder", []map[string]any{
		{"id": 1, "path": path, "accessible": accessible, "freeSpace": int64(500) << 30, "unmappedFolders": folders},
	})
}

// A folder renamed outside Sonarr shows up from both sides: the series has
// lost its folder, and the folder belongs to no series. Both name the same
// fix, and neither offers to add the show again.
func TestAuditFoldersRenamed(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f,
		map[string]any{"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly", "statistics": map[string]any{"episodeFileCount": 6}},
		map[string]any{"id": 2, "title": "Cowboy Bebop", "year": 1998, "path": "/tv/Cowboy Bebop", "statistics": map[string]any{"episodeFileCount": 2}},
		map[string]any{"id": 3, "title": "Severance", "year": 2022, "path": "/tv/Severance", "statistics": map[string]any{"episodeFileCount": 9}},
		// added, never downloaded: Sonarr makes its folder when it imports
		map[string]any{"id": 4, "title": "Breaking Bad", "year": 2008, "path": "/tv/Breaking Bad"},
	)
	rootFolder(t, f, "/tv", true, "Cowboy Bebop (1998)", "The Expanse")
	dirs(t, f, "/tv", map[string]int{"Firefly": 6, "Cowboy Bebop (1998)": 2, "The Expanse": 3})
	f.answer(t, "GET /api/v3/episodefile", []any{})
	f.answer(t, "GET /api/v3/episode", []any{})
	cs := session(t, f, Options{})

	missing := mustCall(t, cs, "audit_missing_folders", nil)
	if got := problems(missing); !slices.Equal(got, []string{"series folder renamed", "series folder missing"}) {
		t.Fatalf("audit_missing_folders = %v", missing)
	}
	rows := rowsOf(missing["findings"])
	if text(rows[0]["series"]) != "Cowboy Bebop" || !strings.Contains(text(rows[0]["detail"]), "/tv/Cowboy Bebop (1998)") ||
		!strings.Contains(text(rows[0]["fix"]), `series_edit path "/tv/Cowboy Bebop (1998)"`) {
		t.Errorf("the renamed folder = %v", rows[0])
	}
	if text(rows[1]["series"]) != "Severance" || !strings.Contains(text(rows[1]["detail"]), "no folder Sonarr does not know") ||
		!strings.Contains(text(rows[1]["detail"]), "records 9 files there") {
		t.Errorf("the missing folder = %v", rows[1])
	}

	unmapped := mustCall(t, cs, "audit_unmapped_folders", nil)
	if got := problems(unmapped); !slices.Equal(got, []string{"folder of a series that moved", "folder not in Sonarr"}) {
		t.Fatalf("audit_unmapped_folders = %v", unmapped)
	}
	rows = rowsOf(unmapped["findings"])
	if !strings.Contains(text(rows[0]["fix"]), "series_edit Cowboy Bebop") || strings.Contains(text(rows[0]["fix"]), "series_import with") {
		t.Errorf("the moved folder's fix = %v", rows[0]["fix"])
	}
	if text(rows[1]["subject"]) != "/tv/The Expanse" || !strings.Contains(text(rows[1]["detail"]), "3 video files") ||
		!strings.Contains(text(rows[1]["fix"]), "series_import") {
		t.Errorf("the unknown folder = %v", rows[1])
	}

	// the files of a series whose folder is gone are not reported twice
	files := mustCall(t, cs, "audit_missing_files", nil)
	if number(files["total_findings"]) != 0 {
		t.Errorf("audit_missing_files = %v", files)
	}
}

// A root folder Sonarr can read that holds none of its series is one
// finding, not one per series: a drive that is not mounted.
func TestAuditFoldersEmptyRoot(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f,
		map[string]any{"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly", "statistics": map[string]any{"episodeFileCount": 6}},
		map[string]any{"id": 2, "title": "Severance", "year": 2022, "path": "/tv/Severance", "statistics": map[string]any{"episodeFileCount": 9}},
	)
	rootFolder(t, f, "/tv", true)
	dirs(t, f, "/tv", map[string]int{})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_missing_folders", nil)
	if got := problems(out); !slices.Equal(got, []string{"root folder holds none of its series"}) {
		t.Fatalf("audit_missing_folders = %v", out)
	}
	if row := rowsOf(out["findings"])[0]; !strings.Contains(text(row["detail"]), "2 series folders") ||
		!strings.Contains(text(row["detail"]), "a drive not mounted") {
		t.Errorf("the empty root = %v", row)
	}
}

// A root folder Sonarr cannot read at all says nothing about the series
// under it: audit_health reports the root folder itself.
func TestAuditFoldersUnreachableRoot(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly", "statistics": map[string]any{"episodeFileCount": 6}})
	rootFolder(t, f, "/tv", false)
	f.answer(t, "GET /api/v3/health", []any{})
	f.answer(t, "GET /api/v3/diskspace", []any{})
	cs := session(t, f, Options{})

	if out := mustCall(t, cs, "audit_missing_folders", nil); number(out["total_findings"]) != 0 {
		t.Errorf("audit_missing_folders = %v", out)
	}
	if got := problems(mustCall(t, cs, "audit_health", nil)); !slices.Equal(got, []string{"root folder unreachable"}) {
		t.Errorf("audit_health = %v", got)
	}
	if n := len(f.requests("/api/v3/filesystem")); n != 0 {
		t.Errorf("%d folder listings for a root folder Sonarr cannot read", n)
	}
}

// Every kind of finding is a named constant listed in AuditProblems: the
// live suite checks the seeded library reports each one, and a kind written
// as a bare string in an audit would slip past that check. These read the
// package's own source, because that is where the mistake would be.
func TestFindingKindsAreRegistered(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("audit*.go")
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, kinds := range AuditProblems {
		for _, kind := range kinds {
			listed[kind] = true
		}
	}
	declared := regexp.MustCompile(`(?m)^\s*(problem[A-Za-z]+)\s*=\s*"([^"]+)"`)
	// a Problem set to something in quotes, rather than to one of the constants
	inline := regexp.MustCompile(`(?:Problem:|\.Problem\s*=|\.Problem, \w+\.\w+ =|problem\s*:?=|return)\s*"([^"]+)"`)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file) //nolint:gosec // a path from this package's own directory listing
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range declared.FindAllStringSubmatch(string(src), -1) {
			if !listed[m[2]] {
				t.Errorf("%s: %s (%q) is not in AuditProblems, so no test has to report it", file, m[1], m[2])
			}
		}
		for _, m := range inline.FindAllStringSubmatch(string(src), -1) {
			t.Errorf("%s: a finding's problem is written as %q; name it as a problem constant and list it in AuditProblems", file, m[1])
		}
	}
}

// One audit that cannot run does not take the others' counts with it, and
// its own is left out rather than read as none: audit_all names it, says
// why, and counts the rest.
func TestAuditAllKeepsWhatRanAndNamesWhatDidNot(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f)
	f.answerStatus(t, "GET /api/v3/health", http.StatusInternalServerError, map[string]any{"message": "the checks are not ready"})
	f.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch p := strings.TrimPrefix(r.URL.Path, "/api/v3/"); {
		case strings.HasPrefix(p, "config/"):
			writeJSON(t, w, map[string]any{"id": 1})
		case p == "system/status":
			writeJSON(t, w, map[string]any{"version": "4.0.20.3014"})
		case p == "queue" || p == "history" || p == "blocklist" || p == "log" || strings.HasPrefix(p, "wanted/"):
			writeJSON(t, w, page())
		default:
			writeJSON(t, w, []any{})
		}
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_all", nil)
	if failed := strs(out["failed"]); !slices.Equal(failed, []string{"audit_health"}) {
		t.Fatalf("failed = %v, want the one audit whose read failed", failed)
	}
	audits := rowsOf(out["audits"])
	if len(audits) < 10 {
		t.Fatalf("%d audits answered, want every one of them", len(audits))
	}
	for _, row := range audits {
		_, counted := row["findings"]
		switch name := text(row["audit"]); {
		case name == "audit_health" && (counted || !strings.Contains(text(row["error"]), "the checks are not ready")):
			t.Errorf("the audit that failed = %v, want its error and no count", row)
		case name != "audit_health" && (!counted || text(row["error"]) != ""):
			t.Errorf("%s = %v, want its count", name, row)
		}
	}
}

// Each audit tool registered is in AuditProblems, and nothing else is.
func TestAuditProblemsCoverTheAudits(t *testing.T) {
	t.Parallel()

	registered := map[string]bool{}
	for _, name := range register(t, Options{Toolsets: []string{"all"}}) {
		if strings.HasPrefix(name, "audit_") && name != "audit_all" {
			registered[name] = true
		}
	}
	for name := range registered {
		if len(AuditProblems[name]) == 0 {
			t.Errorf("%s reports no kind of finding in AuditProblems", name)
		}
	}
	for name := range AuditProblems {
		if !registered[name] {
			t.Errorf("AuditProblems has %s, which is not an audit this server registers", name)
		}
	}
}

// The runtime audit against files the seeded library cannot hold: one far longer than its episode, one cut short, and one Sonarr could read no
// running time from. The long one is listed last and is not called a truncated download: it is as often the right file, two parts in one.
func TestAuditRuntimeEdges(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{
		"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly", "runtime": 45,
		"statistics": map[string]any{"episodeFileCount": 4},
	})
	file := func(id, episode int, runTime string) map[string]any {
		return map[string]any{
			"id": id, "seriesId": 1, "seasonNumber": 1, "path": fmt.Sprintf("/tv/Firefly/S01E%02d.mkv", episode),
			"mediaInfo": map[string]any{"runTime": runTime, "resolution": "1920x1080"},
		}
	}
	f.answer(t, "GET /api/v3/episodefile", []map[string]any{
		file(11, 1, "00:45:03"), file(12, 2, "01:31:00"), file(13, 3, ""), file(14, 4, "00:10:00"),
	})
	f.answer(t, "GET /api/v3/episode", []map[string]any{
		{"id": 1, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 1, "episodeFileId": 11, "runtime": 45, "hasFile": true},
		{"id": 2, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 2, "episodeFileId": 12, "runtime": 45, "hasFile": true},
		{"id": 3, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 3, "episodeFileId": 13, "runtime": 45, "hasFile": true},
		{"id": 4, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 4, "episodeFileId": 14, "runtime": 45, "hasFile": true},
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_runtime", nil)
	rows := rowsOf(out["findings"])
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		order = append(order, text(row["problem"]))
	}
	if !slices.Equal(order, []string{"no running time", "shorter than it should be", "longer than it should be"}) {
		t.Fatalf("audit_runtime lists %v, want the long file after the others: %v", order, out)
	}
	if !strings.Contains(text(rows[0]["detail"]), "could not read a running time") {
		t.Errorf("the file with no runtime = %v", rows[0]["detail"])
	}
	if short := rows[1]; !strings.Contains(text(short["detail"]), "runs 10 minutes, the episode is 45") || !strings.Contains(text(short["detail"]), "truncated download") || !strings.HasPrefix(text(short["fix"]), "release_search") {
		t.Errorf("the short file = %v", short)
	}
	if long := rows[2]; !strings.Contains(text(long["detail"]), "runs 91 minutes, the episode is 45") || !strings.Contains(text(long["detail"]), "may hold more than this episode") || strings.Contains(text(long["detail"]), "truncated") || !strings.HasPrefix(text(long["fix"]), "check it before replacing it") {
		t.Errorf("the long file = %v", long)
	}
}

// A video file in a series folder that Sonarr's own scan will not account
// for: the audit says so rather than guessing why.
func TestAuditUntrackedFileSonarrIgnores(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{
		"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly",
		"statistics": map[string]any{"episodeFileCount": 1},
	})
	f.answer(t, "GET /api/v3/episodefile", []map[string]any{
		{"id": 11, "seriesId": 1, "seasonNumber": 1, "path": "/tv/Firefly/S01E01.mkv"},
	})
	f.answer(t, "GET /api/v3/filesystem/mediafiles", []map[string]any{
		{"path": "/tv/Firefly/S01E01.mkv", "name": "S01E01.mkv"},
		{"path": "/tv/Firefly/stray.mkv", "name": "stray.mkv"},
	})
	// Sonarr's manual import says nothing about the stray file
	f.answer(t, "GET /api/v3/manualimport", []any{})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_untracked_files", nil)
	if got := problems(out); !slices.Equal(got, []string{"file not tracked"}) {
		t.Fatalf("audit_untracked_files = %v", out)
	}
	if row := rowsOf(out["findings"])[0]; text(row["subject"]) != "stray.mkv" || !strings.Contains(text(row["fix"]), "import_scan") {
		t.Errorf("the file Sonarr ignores = %v", row)
	}
}

// Sonarr parses a whole folder to say why it tracks none of the files in it, which is the slow part of this audit: a folder is asked about only
// while its findings will be listed, and past the limit its untracked files are counted without asking.
func TestAuditUntrackedFilesAsksOnlyAboutWhatItLists(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f,
		map[string]any{"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly"},
		map[string]any{"id": 2, "title": "Zorro", "year": 1957, "path": "/tv/Zorro"},
		map[string]any{"id": 3, "title": "Zulu", "year": 1964, "path": "/tv/Zulu"},
	)
	f.mux.HandleFunc("GET /api/v3/filesystem/mediafiles", func(w http.ResponseWriter, r *http.Request) {
		folder := r.URL.Query().Get("path")
		writeJSON(t, w, []map[string]any{{"path": folder + "/stray one.mkv"}, {"path": folder + "/stray two.mkv"}})
	})
	f.answer(t, "GET /api/v3/episodefile", []any{})
	f.answer(t, "GET /api/v3/manualimport", []any{})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_untracked_files", map[string]any{"limit": 2})
	if rows := rowsOf(out["findings"]); len(rows) != 2 || text(rows[0]["series"]) != "Firefly" || number(out["total_findings"]) != 6 {
		t.Fatalf("audit_untracked_files = %v, want the first folder's two files listed and all six counted", out)
	}
	if asked := f.requests("/api/v3/manualimport"); len(asked) != 1 || !strings.Contains(asked[0].Query, "Firefly") {
		t.Errorf("Sonarr was asked to parse %d folders, want only the one whose files are listed: %v", len(asked), asked)
	}
}

// A talk show typed as a standard series: Sonarr numbers daily shows by air
// date, and the audit says which ones are set up the other way.
func TestAuditSeriesSettingsDaily(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f,
		map[string]any{
			"id": 1, "title": "The Daily Show", "year": 1996, "path": "/tv/The Daily Show", "seriesType": "standard",
			"genres": []string{"Comedy", "Talk Show"},
		},
		map[string]any{
			"id": 2, "title": "The Late Show", "year": 2015, "path": "/tv/The Late Show", "seriesType": "daily",
			"genres": []string{"Talk Show"},
		},
	)
	rootFolder(t, f, "/tv", true)
	dirs(t, f, "/tv", map[string]int{"The Daily Show": 1, "The Late Show": 1})
	f.answer(t, "GET /api/v3/config/naming", map[string]any{"id": 1, "renameEpisodes": true, "seriesFolderFormat": "{Series Title}"})
	// the folder each series would have under the naming format
	f.mux.HandleFunc("GET /api/v3/series/{id}/folder", func(w http.ResponseWriter, r *http.Request) {
		folder := map[string]string{"1": "The Daily Show", "2": "The Late Show"}[r.PathValue("id")]
		writeJSON(t, w, map[string]any{"folder": folder})
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_series_settings", nil)
	if got := problems(out); !slices.Equal(got, []string{"daily show not typed daily"}) {
		t.Fatalf("audit_series_settings = %v", out)
	}
	if row := rowsOf(out["findings"])[0]; text(row["series"]) != "The Daily Show" || !strings.Contains(text(row["fix"]), "series_edit series_type daily") {
		t.Errorf("the talk show = %v", row)
	}
}

// The seasons with episodes missing come from the counts Sonarr keeps for each season, and the episodes are read only for the seasons listed: a
// library with tens of thousands of episodes missing is answered without reading them all. A gap is told from a whole season, a series that is not
// monitored is passed over as Sonarr's own wanted list passes it over, and a season past the limit is counted and not read.
func TestAuditMissingEpisodesReadsOnlyTheSeasonsItLists(t *testing.T) {
	t.Parallel()

	season := func(number, wanted, have int) map[string]any {
		return map[string]any{"seasonNumber": number, "monitored": true, "statistics": map[string]any{"episodeCount": wanted, "episodeFileCount": have, "totalEpisodeCount": wanted + 1}}
	}
	episode := func(id, series, season, number int, more map[string]any) map[string]any {
		e := map[string]any{"id": id, "seriesId": series, "seasonNumber": season, "episodeNumber": number, "monitored": true, "airDate": "2002-09-20", "airDateUtc": fmt.Sprintf("2002-09-%02dT01:00:00Z", 10+number)}
		maps.Copy(e, more)

		return e
	}
	episodes := map[string][]map[string]any{
		"1": {
			episode(11, 1, 1, 1, map[string]any{"hasFile": true, "episodeFileId": 5}), episode(12, 1, 1, 2, map[string]any{"lastSearchTime": ago(time.Hour)}), episode(13, 1, 1, 3, nil),
			episode(14, 1, 1, 4, map[string]any{"airDateUtc": "2999-01-01T00:00:00Z"}), episode(15, 1, 1, 5, map[string]any{"monitored": false}),
			episode(21, 1, 2, 1, nil), episode(22, 1, 2, 2, nil),
		},
		"2": {episode(31, 2, 1, 1, nil)},
		"3": {episode(41, 3, 1, 1, nil)},
	}

	f := newFakeServer(t)
	library(t, f,
		map[string]any{"id": 1, "title": "Firefly", "year": 2002, "monitored": true, "seasons": []any{season(1, 3, 1), season(2, 2, 0), season(3, 4, 4)}},
		map[string]any{"id": 2, "title": "Not Watched", "year": 2000, "monitored": false, "seasons": []any{season(1, 1, 0)}},
		map[string]any{"id": 3, "title": "Zorro", "year": 1957, "monitored": true, "seasons": []any{season(1, 1, 0)}},
	)
	f.mux.HandleFunc("GET /api/v3/episode", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, episodes[r.URL.Query().Get("seriesId")])
	})
	f.answer(t, "GET /api/v3/queue", page(map[string]any{"id": 9, "seriesId": 1, "episodeId": 13, "status": "downloading"}))
	cs := session(t, f, Options{Toolsets: []string{"all"}})

	out := mustCall(t, cs, "audit_missing_episodes", map[string]any{"limit": 2})
	rows := rowsOf(out["findings"])
	if len(rows) != 2 || number(out["total_findings"]) != 3 {
		t.Fatalf("audit_missing_episodes = %v, want two seasons listed of the three with episodes missing", out)
	}
	if gap := rows[0]; text(gap["series"]) != "Firefly" || text(gap["subject"]) != "season 1" || text(gap["problem"]) != "episodes missing" || !strings.HasPrefix(text(gap["detail"]), "2 missing: S01E02, S01E03; oldest aired 2002-09-20") || !strings.Contains(text(gap["detail"]), "1 already downloading, 1 searched for and not found") {
		t.Errorf("the season with a gap = %v", gap)
	}
	if whole := rows[1]; text(whole["subject"]) != "season 2" || text(whole["problem"]) != "whole season missing" || !strings.Contains(text(whole["fix"]), "season_search 2") {
		t.Errorf("the season with nothing = %v", whole)
	}
	for _, asked := range f.requests("/api/v3/episode") {
		if !strings.Contains(asked.Query, "seriesId=1") {
			t.Errorf("episodes were read with %q, want only those of the series listed", asked.Query)
		}
	}
	if len(f.requests("/api/v3/wanted/missing")) != 0 {
		t.Error("Sonarr's whole wanted list was read")
	}

	// one series asked about alone is read the same way
	one := mustCall(t, cs, "audit_missing_episodes", map[string]any{"series": "Zorro"})
	if rows := rowsOf(one["findings"]); len(rows) != 1 || text(rows[0]["series"]) != "Zorro" || number(one["total_findings"]) != 1 {
		t.Errorf("one series asked about = %v", one)
	}
}

// A library whose folders carry the year under a format that does not: the audit says so once and names the format that makes them, where a finding
// a folder said the same of every series. A few folders off the format are still listed one by one, and so is a series asked about on its own.
func TestAuditSeriesSettingsFormatOffAcrossTheLibrary(t *testing.T) {
	t.Parallel()

	// a library of twelve, the first off of them in a folder named otherwise than the format's "Show N"
	audit := func(off int, named func(n int) string, args map[string]any) map[string]any {
		f := newFakeServer(t)
		series := make([]map[string]any, 0, 12)
		for n := 1; n <= 12; n++ {
			folder := fmt.Sprintf("Show %d", n)
			if n <= off {
				folder = named(n)
			}
			series = append(series, map[string]any{"id": n, "title": fmt.Sprintf("Show %d", n), "year": 2000 + n, "path": "/tv/" + folder, "seriesType": "standard"})
		}
		// and one with something else wrong with it, to see where the format's finding is listed
		series[11]["genres"] = []string{"Talk Show"}
		library(t, f, series...)
		rootFolder(t, f, "/tv", true)
		f.mux.HandleFunc("GET /api/v3/series/{id}/folder", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"folder": "Show " + r.PathValue("id")})
		})

		return mustCall(t, session(t, f, Options{}), "audit_series_settings", args)
	}
	withYear := func(n int) string { return fmt.Sprintf("Show %d (%d)", n, 2000+n) }

	out := audit(12, withYear, nil)
	rows := rowsOf(out["findings"])
	if len(rows) != 2 || number(out["total_findings"]) != 2 || text(rows[1]["problem"]) != "daily show not typed daily" {
		t.Fatalf("a library all off the format = %v, want it said once and first, before the one series with something else wrong", out)
	}
	if row := rows[0]; text(row["subject"]) != "series folder format" || text(row["problem"]) != "folder not named to the format" || !strings.Contains(text(row["detail"]), "12 of 12 series") || !strings.Contains(text(row["detail"]), `"Show 1 (2001)"`) || !strings.Contains(text(row["fix"]), `seriesFolderFormat "{Series TitleYear}"`) {
		t.Errorf("the one finding = %v, want the count, an example and the format that adds the year", row)
	}

	// off in a way no format is known for: still once, with the setting named and no format guessed
	other := rowsOf(audit(12, func(n int) string { return fmt.Sprintf("show.%d", n) }, nil)["findings"])
	if len(other) != 2 || strings.Contains(text(other[0]["fix"]), "TitleYear") || !strings.Contains(text(other[0]["fix"]), "seriesFolderFormat set to how the folders are named") {
		t.Errorf("folders off the format another way = %v", other)
	}

	// a few folders are folders to rename, each with its own fix, listed with whatever else is wrong with its series (which are read by title)
	few := rowsOf(audit(3, withYear, nil)["findings"])
	if len(few) != 4 || text(few[0]["series"]) != "Show 1" || !strings.Contains(text(few[0]["fix"]), "series_edit path /tv/Show 1 with move_files") || text(few[1]["series"]) != "Show 12" || text(few[1]["problem"]) != "daily show not typed daily" {
		t.Errorf("three folders off the format = %v, want each listed", few)
	}

	// and so is one series asked about alone, whatever the rest of the library does
	one := rowsOf(audit(12, withYear, map[string]any{"series": "Show 2"})["findings"])
	if len(one) != 1 || text(one[0]["series"]) != "Show 2" || !strings.Contains(text(one[0]["fix"]), "series_edit path /tv/Show 2 with move_files") {
		t.Errorf("one series asked about = %v, want its own finding", one)
	}
}

// The language audit on files whose audio says nothing: recorded in another
// language, and recorded as a language Sonarr could not tell.
func TestAuditLanguageEdges(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{
		"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly",
		"originalLanguage": map[string]any{"id": 1, "name": "English"},
		"statistics":       map[string]any{"episodeFileCount": 3},
	})
	file := func(id int, languages []map[string]any, audio string) map[string]any {
		return map[string]any{
			"id": id, "seriesId": 1, "seasonNumber": 1, "path": fmt.Sprintf("/tv/Firefly/S01E%02d.mkv", id),
			"languages": languages, "mediaInfo": map[string]any{"runTime": "00:45:00", "audioLanguages": audio},
		}
	}
	english := []map[string]any{{"id": 1, "name": "English"}}
	french := []map[string]any{{"id": 2, "name": "French"}}
	unknown := []map[string]any{{"id": 0, "name": "Unknown"}}
	f.answer(t, "GET /api/v3/episodefile", []map[string]any{
		file(1, english, ""), file(2, french, ""), file(3, unknown, ""),
	})
	f.answer(t, "GET /api/v3/episode", []map[string]any{
		{"id": 1, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 1, "episodeFileId": 1, "hasFile": true},
		{"id": 2, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 2, "episodeFileId": 2, "hasFile": true},
		{"id": 3, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 3, "episodeFileId": 3, "hasFile": true},
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_language", nil)
	// the English one is left alone: nothing says it is not what it claims
	if got := problems(out); !slices.Equal(got, []string{"recorded in another language", "language unknown"}) {
		t.Fatalf("audit_language = %v", out)
	}
	rows := rowsOf(out["findings"])
	if !strings.Contains(text(rows[0]["detail"]), "recorded as French, not English") ||
		!strings.Contains(text(rows[0]["fix"]), "file_edit file 2") {
		t.Errorf("the French recording = %v", rows[0])
	}
	if !strings.Contains(text(rows[1]["fix"]), "file_edit") {
		t.Errorf("the unknown recording = %v", rows[1])
	}
}

// The files below their profile's quality cutoff are found in each series' own files, where Sonarr marks each one, and not in Sonarr's Cutoff Unmet
// list, which is every such episode in the library with its file. What that list leaves out is left out here too: a series that is not monitored,
// and a file whose episodes are not; and a file holding two episodes is one file.
func TestAuditCutoffReadsEachSeriesFiles(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f,
		map[string]any{"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly", "qualityProfileId": 4, "monitored": true, "statistics": map[string]any{"episodeFileCount": 4}},
		map[string]any{"id": 2, "title": "Not Watched", "year": 2000, "path": "/tv/Not Watched", "qualityProfileId": 4, "monitored": false, "statistics": map[string]any{"episodeFileCount": 1}},
		map[string]any{"id": 3, "title": "No Files", "year": 2001, "path": "/tv/No Files", "qualityProfileId": 4, "monitored": true, "statistics": map[string]any{"episodeFileCount": 0}},
	)
	f.answer(t, "GET /api/v3/qualityprofile", []map[string]any{{
		"id": 4, "name": "HD-1080p", "upgradeAllowed": true, "cutoff": 3,
		"items": []map[string]any{{"quality": map[string]any{"id": 1, "name": "SDTV"}, "allowed": true}, {"quality": map[string]any{"id": 3, "name": "WEBDL-1080p"}, "allowed": true}},
	}})
	file := func(id, series int, quality string, below bool) map[string]any {
		return map[string]any{"id": id, "seriesId": series, "seasonNumber": 1, "path": fmt.Sprintf("/tv/%d/%d.mkv", series, id), "qualityCutoffNotMet": below, "quality": map[string]any{"quality": map[string]any{"name": quality}}}
	}
	episode := func(id, series, number, fileID int, monitored bool) map[string]any {
		return map[string]any{"id": id, "seriesId": series, "seasonNumber": 1, "episodeNumber": number, "episodeFileId": fileID, "monitored": monitored, "hasFile": true}
	}
	files := map[string][]map[string]any{
		"1": {file(11, 1, "SDTV", true), file(12, 1, "SDTV", true), file(13, 1, "WEBDL-1080p", false), file(14, 1, "SDTV", true)},
		"2": {file(21, 2, "SDTV", true)},
	}
	episodes := map[string][]map[string]any{
		// a file of two episodes, one at the cutoff already, and one below it whose episode is not monitored
		"1": {episode(1, 1, 1, 11, true), episode(2, 1, 2, 12, true), episode(3, 1, 3, 12, true), episode(4, 1, 4, 13, true), episode(5, 1, 5, 14, false)},
		"2": {episode(6, 2, 1, 21, true)},
	}
	f.mux.HandleFunc("GET /api/v3/episodefile", func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, files[r.URL.Query().Get("seriesId")]) })
	f.mux.HandleFunc("GET /api/v3/episode", func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, episodes[r.URL.Query().Get("seriesId")]) })
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_cutoff_unmet", nil)
	rows := rowsOf(out["findings"])
	if len(rows) != 1 || number(out["total_findings"]) != 1 {
		t.Fatalf("audit_cutoff_unmet = %v, want the one monitored series with files below cutoff", out)
	}
	if row := rows[0]; text(row["series"]) != "Firefly" || text(row["subject"]) != "2 files" || text(row["problem"]) != "below quality cutoff" || !strings.Contains(text(row["detail"]), "2 below HD-1080p's cutoff of WEBDL-1080p (2 SDTV): S01E01 SDTV, S01E02-E03 SDTV") || !strings.HasPrefix(text(row["fix"]), "series_search") {
		t.Errorf("the series below cutoff = %v", row)
	}
	if len(f.requests("/api/v3/wanted/cutoff")) != 0 {
		t.Error("Sonarr's whole Cutoff Unmet list was read")
	}
	for _, asked := range append(f.requests("/api/v3/episodefile"), f.requests("/api/v3/episode")...) {
		if !strings.Contains(asked.Query, "seriesId=1") {
			t.Errorf("a read was made with %q, want only the monitored series with files", asked.Query)
		}
	}
}

// A file good enough on quality and short of the custom format score its
// profile upgrades until: Sonarr's own Cutoff Unmet list is quality alone,
// so the audit sweeps for these itself.
func TestAuditCutoffFormatScore(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{
		"id": 1, "title": "Chernobyl", "year": 2019, "path": "/tv/Chernobyl", "qualityProfileId": 4, "monitored": true,
		"statistics": map[string]any{"episodeFileCount": 2},
	})
	f.answer(t, "GET /api/v3/qualityprofile", []map[string]any{{
		"id": 4, "name": "HD-1080p", "upgradeAllowed": false, "cutoff": 3, "cutoffFormatScore": 10,
		"items": []map[string]any{{"quality": map[string]any{"id": 3, "name": "WEBDL-1080p"}, "allowed": true}},
	}})
	f.answer(t, "GET /api/v3/episodefile", []map[string]any{
		{"id": 11, "seriesId": 1, "seasonNumber": 1, "path": "/tv/Chernobyl/S01E01.mkv", "customFormatScore": 0, "qualityCutoffNotMet": false, "quality": map[string]any{"quality": map[string]any{"id": 3, "name": "WEBDL-1080p"}}},
		{"id": 12, "seriesId": 1, "seasonNumber": 1, "path": "/tv/Chernobyl/S01E02.mkv", "customFormatScore": 25, "qualityCutoffNotMet": false, "quality": map[string]any{"quality": map[string]any{"id": 3, "name": "WEBDL-1080p"}}},
	})
	f.answer(t, "GET /api/v3/episode", []map[string]any{
		{"id": 1, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 1, "episodeFileId": 11, "monitored": true, "hasFile": true},
		{"id": 2, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 2, "episodeFileId": 12, "monitored": true, "hasFile": true},
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_cutoff_unmet", nil)
	if got := problems(out); !slices.Equal(got, []string{"below custom format cutoff"}) {
		t.Fatalf("audit_cutoff_unmet = %v", out)
	}
	row := rowsOf(out["findings"])[0]
	// only the file scoring under 10, and the profile's own setting is said
	if !strings.Contains(text(row["detail"]), "S01E01 WEBDL-1080p (score 0)") || strings.Contains(text(row["detail"]), "S01E02") {
		t.Errorf("the files below the score = %v", row["detail"])
	}
	if !strings.Contains(text(row["detail"]), "Upgrades Allowed off") || !strings.Contains(text(row["fix"]), "series_search") {
		t.Errorf("the finding = %v", row)
	}
}

// A series with no files is one of five things, each put right a different way: wanted and never fetched, with nothing of it monitored, with files
// in its folder Sonarr does not track, with no episode Sonarr knows of, or with no air dates. A series with files is not this audit's business, and
// nor is one that has not aired yet, whether the series list says so or its episodes do.
func TestAuditNoFilesSaysWhich(t *testing.T) {
	t.Parallel()

	episode := func(id, series, season, number int, more map[string]any) map[string]any {
		e := map[string]any{"id": id, "seriesId": series, "seasonNumber": season, "episodeNumber": number, "monitored": true, "airDateUtc": "2002-09-20T01:00:00Z"}
		maps.Copy(e, more)

		return e
	}
	later, off, undated := map[string]any{"airDateUtc": "2999-01-01T00:00:00Z"}, map[string]any{"monitored": false}, map[string]any{"airDateUtc": ""}
	episodes := map[string][]map[string]any{
		// a special that aired ahead of the show, which nobody monitors, is not the show having aired
		"1":  {episode(11, 1, 1, 1, map[string]any{"airDateUtc": "2999-02-01T00:00:00Z"}), episode(12, 1, 1, 2, later), episode(13, 1, 0, 1, off)},
		"2":  {episode(21, 2, 1, 1, later)},
		"10": {episode(101, 10, 1, 1, undated), episode(102, 10, 1, 2, undated)},
		"11": {episode(111, 11, 0, 1, off), episode(112, 11, 0, 2, off)},
		"4":  {episode(41, 4, 1, 1, nil)},
		"6":  {episode(61, 6, 1, 1, nil), episode(62, 6, 1, 2, nil)},
		"7":  {episode(71, 7, 1, 1, nil), episode(72, 7, 1, 2, nil), episode(73, 7, 1, 3, nil), episode(74, 7, 1, 4, off), episode(75, 7, 1, 5, later), episode(76, 7, 0, 1, off)},
		"8":  {episode(81, 8, 1, 1, nil), episode(82, 8, 1, 2, nil)},
		"9":  {episode(91, 9, 1, 1, off), episode(92, 9, 1, 2, off)},
	}

	f := newFakeServer(t)
	library(t, f,
		map[string]any{"id": 1, "title": "Airing Later", "path": "/tv/Airing Later", "monitored": true, "status": "upcoming"},
		map[string]any{"id": 2, "title": "Airing Unwatched", "path": "/tv/Airing Unwatched", "monitored": false},
		map[string]any{"id": 3, "title": "Complete", "path": "/tv/Complete", "monitored": true, "statistics": map[string]any{"episodeFileCount": 3}},
		map[string]any{"id": 4, "title": "Copied In", "path": "/tv/Copied In", "monitored": true},
		map[string]any{"id": 5, "title": "Empty", "path": "/tv/Empty", "monitored": true, "status": "deleted"},
		map[string]any{"id": 6, "title": "Every One Coming", "path": "/tv/Every One Coming", "monitored": true},
		map[string]any{"id": 7, "title": "Never Fetched", "path": "/tv/Never Fetched", "monitored": true, "added": "2023-04-01T10:00:00Z", "statistics": map[string]any{"episodeCount": 3}},
		map[string]any{"id": 8, "title": "Not Watched", "path": "/tv/Not Watched", "monitored": false},
		map[string]any{"id": 9, "title": "Seasons Off", "path": "/tv/Seasons Off", "monitored": true},
		map[string]any{"id": 10, "title": "Undated", "path": "/tv/Undated", "monitored": true, "status": "ended"},
		map[string]any{"id": 11, "title": "Specials Only", "path": "/tv/Specials Only", "monitored": true},
		map[string]any{"id": 12, "title": "Empty With Files", "path": "/tv/Empty With Files", "monitored": true},
		// the series list alone says these have not aired: one Sonarr calls upcoming, one whose first episode airs later
		map[string]any{"id": 13, "title": "Premieres Soon", "path": "/tv/Premieres Soon", "monitored": true, "firstAired": "2999-03-01T00:00:00Z"},
		map[string]any{"id": 14, "title": "Announced", "path": "/tv/Announced", "monitored": true, "status": "upcoming"},
	)
	f.mux.HandleFunc("GET /api/v3/episode", func(w http.ResponseWriter, r *http.Request) {
		eps := episodes[r.URL.Query().Get("seriesId")]
		if eps == nil {
			eps = []map[string]any{}
		}
		writeJSON(t, w, eps)
	})
	f.mux.HandleFunc("GET /api/v3/filesystem/mediafiles", func(w http.ResponseWriter, r *http.Request) {
		files := []map[string]any{}
		if folder := r.URL.Query().Get("path"); folder == "/tv/Copied In" || folder == "/tv/Empty With Files" {
			files = append(files, map[string]any{"path": folder + "/S01E01.mkv"}, map[string]any{"path": folder + "/S01E02.mkv"}, map[string]any{"path": folder + "/extras/making of.mkv"})
		}
		writeJSON(t, w, files)
	})
	f.answer(t, "GET /api/v3/queue", page(
		map[string]any{"id": 1, "seriesId": 6, "episodeId": 61}, map[string]any{"id": 2, "seriesId": 6, "episodeId": 62}, map[string]any{"id": 3, "seriesId": 7, "episodeId": 72},
	))
	cs := session(t, f, Options{Toolsets: []string{"all"}})

	out := mustCall(t, cs, "audit_no_files", nil)
	want := []struct{ series, problem, detail, fix string }{
		{"Copied In", "files in the folder, none tracked", "holds 2 video files, and Sonarr tracks none", "import_apply"},
		{"Empty", "no episodes known", "lists no episode of it, so there is nothing for it to fetch; TheTVDB has removed the show", "if it stays empty, series_delete"},
		{"Empty With Files", "no episodes known", "its folder holds 2 video files, which Sonarr has no episode to match to", "series_import adds the folder as the right one"},
		{"Every One Coming", "no files", "wants 2 episodes that aired and has a file for none, 2 downloading now", "nothing yet: every one is downloading"},
		{"Never Fetched", "no files", "wants 3 episodes that aired and has a file for none, 1 downloading now; added 2023-04-01", "series_search"},
		{"Not Watched", "no files and nothing wanted", "2 episodes aired and Sonarr will fetch none: the series is not monitored", "series_edit monitored true, then series_search; or series_delete"},
		{"Seasons Off", "no files and nothing wanted", "2 episodes aired and Sonarr will fetch none: not one is monitored", "season_monitor the seasons wanted"},
		// a show that is all specials has aired when they have
		{"Specials Only", "no files and nothing wanted", "2 episodes aired and Sonarr will fetch none: not one is monitored", "season_monitor the seasons wanted"},
		{"Undated", "no air dates", "an air date for none of its 2 episodes, so it never looks for one", "series_refresh"},
	}
	rows := rowsOf(out["findings"])
	if len(rows) != len(want) || number(out["total_findings"]) != len(want) || number(out["scanned"]) != 14 {
		t.Fatalf("audit_no_files = %v, want the %d series with no files that have aired, of the 14", out, len(want))
	}
	for i, w := range want {
		row := rows[i]
		if text(row["series"]) != w.series || text(row["problem"]) != w.problem || !strings.Contains(text(row["detail"]), w.detail) || !strings.Contains(text(row["fix"]), w.fix) {
			t.Errorf("finding %d = %v, want %s as %q with %q and %q", i, row, w.series, w.problem, w.detail, w.fix)
		}
		if text(row["subject"]) != "/tv/"+w.series {
			t.Errorf("%s is about %q, want its folder", w.series, row["subject"])
		}
	}
	for _, asked := range f.requests("/api/v3/episode") {
		if strings.Contains(asked.Query, "seriesId=3") {
			t.Error("the episodes of a series with files were read")
		}
	}

	// one series asked about alone, with files and without
	if one := mustCall(t, cs, "audit_no_files", map[string]any{"series": "Complete"}); number(one["total_findings"]) != 0 {
		t.Errorf("a series with files = %v, want nothing", one)
	}
	if one := mustCall(t, cs, "audit_no_files", map[string]any{"series": "Not Watched"}); !slices.Equal(problems(one), []string{"no files and nothing wanted"}) {
		t.Errorf("one series with no files = %v", one)
	}
}

// Which series have no files is in the series list, so every one is counted, and only the ones listed have their episodes and folder read.
func TestAuditNoFilesReadsOnlyTheSeriesItLists(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f,
		map[string]any{"id": 1, "title": "Firefly", "path": "/tv/Firefly", "monitored": true},
		map[string]any{"id": 2, "title": "Has Files", "path": "/tv/Has Files", "monitored": true, "statistics": map[string]any{"episodeFileCount": 1}},
		map[string]any{"id": 3, "title": "Zorro", "path": "/tv/Zorro", "monitored": true},
		map[string]any{"id": 4, "title": "Zulu", "path": "/tv/Zulu", "monitored": true},
	)
	f.answer(t, "GET /api/v3/episode", []map[string]any{{"id": 11, "seasonNumber": 1, "episodeNumber": 1, "monitored": true, "airDateUtc": "2002-09-20T01:00:00Z"}})
	f.answer(t, "GET /api/v3/filesystem/mediafiles", []any{})
	f.answer(t, "GET /api/v3/queue", page())
	cs := session(t, f, Options{Toolsets: []string{"all"}})

	out := mustCall(t, cs, "audit_no_files", map[string]any{"limit": 1})
	if rows := rowsOf(out["findings"]); len(rows) != 1 || text(rows[0]["series"]) != "Firefly" || number(out["total_findings"]) != 3 {
		t.Fatalf("audit_no_files = %v, want the first listed and all three counted", out)
	}
	if asked := f.requests("/api/v3/episode"); len(asked) != 1 || !strings.Contains(asked[0].Query, "seriesId=1") {
		t.Errorf("episodes were read %d times, want once, for the series listed: %v", len(asked), asked)
	}
	if asked := f.requests("/api/v3/filesystem/mediafiles"); len(asked) != 1 || !strings.Contains(asked[0].Query, "Firefly") {
		t.Errorf("folders were read %d times, want once, for the series listed: %v", len(asked), asked)
	}

	// and a library where every series has files is not asked anything more
	full := newFakeServer(t)
	library(t, full, map[string]any{"id": 1, "title": "Has Files", "path": "/tv/Has Files", "statistics": map[string]any{"episodeFileCount": 1}})
	if out := mustCall(t, session(t, full, Options{Toolsets: []string{"all"}}), "audit_no_files", nil); number(out["total_findings"]) != 0 || number(out["scanned"]) != 1 {
		t.Errorf("a library with no series short of files = %v", out)
	}
}
