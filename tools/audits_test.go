package tools

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
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
	f.mux.HandleFunc("DELETE /api/v3/episodefile/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	cs := session(t, f, Options{EnableDelete: true})

	if msg := mustFail(t, cs, "file_delete", map[string]any{"file_ids": []any{7, 8}}); !strings.Contains(msg, "no episode file 8") {
		t.Errorf("the refusal = %q", msg)
	}
	if deleted := f.sent(http.MethodDelete, "/api/v3/episodefile/7"); len(deleted) != 0 {
		t.Error("file 7 was deleted though file 8 does not exist")
	}

	out := mustCall(t, cs, "file_delete", map[string]any{"file_ids": []any{7}})
	if got := strs(out["deleted"]); !slices.Equal(got, []string{"/tv/Firefly/Season 1/Firefly - S01E01.mkv"}) {
		t.Errorf("deleted = %v", got)
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
		case c.want != 0 && (!ok || got.TvdbId != c.want):
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

// The runtime audit against files the seeded library cannot hold: one far
// longer than its episode, and one Sonarr could read no running time from.
func TestAuditRuntimeEdges(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{
		"id": 1, "title": "Firefly", "year": 2002, "path": "/tv/Firefly", "runtime": 45,
		"statistics": map[string]any{"episodeFileCount": 3},
	})
	file := func(id, episode int, runTime string) map[string]any {
		return map[string]any{
			"id": id, "seriesId": 1, "seasonNumber": 1, "path": fmt.Sprintf("/tv/Firefly/S01E%02d.mkv", episode),
			"mediaInfo": map[string]any{"runTime": runTime, "resolution": "1920x1080"},
		}
	}
	f.answer(t, "GET /api/v3/episodefile", []map[string]any{
		file(11, 1, "00:45:03"), file(12, 2, "01:31:00"), file(13, 3, ""),
	})
	f.answer(t, "GET /api/v3/episode", []map[string]any{
		{"id": 1, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 1, "episodeFileId": 11, "runtime": 45, "hasFile": true},
		{"id": 2, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 2, "episodeFileId": 12, "runtime": 45, "hasFile": true},
		{"id": 3, "seriesId": 1, "seasonNumber": 1, "episodeNumber": 3, "episodeFileId": 13, "runtime": 45, "hasFile": true},
	})
	cs := session(t, f, Options{})

	out := mustCall(t, cs, "audit_runtime", nil)
	if got := problems(out); !slices.Equal(got, []string{"longer than it should be", "no running time"}) {
		t.Fatalf("audit_runtime = %v", out)
	}
	rows := rowsOf(out["findings"])
	if !strings.Contains(text(rows[0]["detail"]), "runs 91 minutes, the episode is 45") {
		t.Errorf("the long file = %v", rows[0]["detail"])
	}
	if !strings.Contains(text(rows[1]["detail"]), "could not read a running time") {
		t.Errorf("the file with no runtime = %v", rows[1]["detail"])
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

// A file good enough on quality and short of the custom format score its
// profile upgrades until: Sonarr's own Cutoff Unmet list is quality alone,
// so the audit sweeps for these itself.
func TestAuditCutoffFormatScore(t *testing.T) {
	t.Parallel()

	f := newFakeServer(t)
	library(t, f, map[string]any{
		"id": 1, "title": "Chernobyl", "year": 2019, "path": "/tv/Chernobyl", "qualityProfileId": 4,
		"statistics": map[string]any{"episodeFileCount": 2},
	})
	f.answer(t, "GET /api/v3/qualityprofile", []map[string]any{{
		"id": 4, "name": "HD-1080p", "upgradeAllowed": false, "cutoff": 3, "cutoffFormatScore": 10,
		"items": []map[string]any{{"quality": map[string]any{"id": 3, "name": "WEBDL-1080p"}, "allowed": true}},
	}})
	// Sonarr lists nothing as cutoff unmet: both files are 1080p
	f.answer(t, "GET /api/v3/wanted/cutoff", page())
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
