//go:build acceptance

package tools_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/go-kt/test/services/sabnzbd"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// The downloads, end to end: release_search against the fake indexer,
// release_grab into the fake SABnzbd, the queue as the download moves, and
// what Sonarr does with it when it completes, fails, stalls or will not
// import. Every download is Breaking Bad's (or Firefly's missing E07/E08),
// which only these tests touch.

// grabEpisode searches for an episode and grabs the release Sonarr approves
// first, returning the download the fake client received.
func grabEpisode(t *testing.T, series, episode string) sabnzbd.Job {
	t.Helper()

	found := call(t, "release_search", map[string]any{"series": series, "episode": episode})
	releases := acctest.Rows(t, found["releases"], "releases")
	if len(releases) == 0 || !acctest.BoolOf(releases[0]["approved"]) {
		t.Fatalf("no approved release for %s %s: %v", series, episode, found)
	}
	grab := call(t, "release_grab", map[string]any{"guid": acctest.Str(releases[0]["guid"]), "indexer_id": acctest.Num(t, releases[0]["indexer_id"], "indexer_id")})
	grabbed := acctest.Object(t, grab["grabbed"], "grabbed")
	if acctest.Str(grabbed["episode"]) != episode || acctest.Str(grabbed["series"]) != series || acctest.Str(grabbed["download_client"]) != fakeClientName {
		t.Fatalf("release_grab = %v", grab)
	}
	// the job is named for the release: Breaking.Bad.S01E01.Pilot...
	job, ok := sab.FindJob(strings.ReplaceAll(series, " ", ".") + "." + episode)
	if !ok {
		t.Fatalf("the fake client has no job for %s: %v", episode, sab.Jobs())
	}
	t.Cleanup(func() { clearDownload(t, job.ID) })

	return job
}

// refreshDownloads has Sonarr check the download client now, rather than at
// its next minute.
func refreshDownloads(t *testing.T) {
	t.Helper()

	call(t, "task_run", map[string]any{"task": "RefreshMonitoredDownloads"})
}

// queued is the queue row for a download, or nil.
func queued(t *testing.T, downloadID string) map[string]any {
	t.Helper()

	for _, d := range acctest.Rows(t, call(t, "queue_list", nil)["downloads"], "downloads") {
		if strings.EqualFold(acctest.Str(d["download_id"]), downloadID) {
			return d
		}
	}

	return nil
}

// waitQueued waits for a download to reach a state in the queue.
func waitQueued(t *testing.T, downloadID string, check func(map[string]any) bool) map[string]any {
	t.Helper()

	var row map[string]any
	eventually(t, "queue_list", nil, "download "+downloadID, func(out map[string]any) bool {
		row = nil
		for _, d := range acctest.RowsOf(out["downloads"]) {
			if strings.EqualFold(acctest.Str(d["download_id"]), downloadID) {
				row = d
			}
		}
		if row == nil || !check(row) {
			refreshDownloads(t)
			return false
		}
		return true
	})

	return row
}

// clearDownload takes a download out of Sonarr's queue and the fake client,
// whatever state it reached.
func clearDownload(t *testing.T, id string) {
	t.Helper()

	if d := queued(t, id); d != nil {
		_, _ = invoke("queue_remove", map[string]any{"ids": []any{acctest.Num(t, d["id"], "id")}})
	}
	_ = sab.Remove(id)
}

// completeWith finishes a download with one video of the given length.
func completeWith(t *testing.T, job sabnzbd.Job, minutes int) {
	t.Helper()

	if err := sab.CompleteWith(job.ID, func(dir string) error {
		return fakeVideo(filepath.Join(dir, job.Name+".mkv"), minutes, "1920x1080")
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseSearch(t *testing.T) {
	out := call(t, "release_search", map[string]any{"series": breakingBad.Title, "episode": "S01E01"})

	releases := acctest.Rows(t, out["releases"], "releases")
	if acctest.Num(t, out["total"], "total") != 3 || acctest.Num(t, out["approved"], "approved") != 1 {
		t.Fatalf("Breaking Bad S01E01 = %v", out)
	}
	best := releases[0]
	if !strings.Contains(acctest.Str(best["title"]), "1080p.WEB-DL") || !acctest.BoolOf(best["approved"]) || acctest.Str(best["quality"]) != "WEBDL-1080p" ||
		acctest.Str(best["episodes"]) != "S01E01" || acctest.Str(best["indexer"]) != fakeIndexerName || acctest.Str(best["protocol"]) != "usenet" {
		t.Errorf("the approved release = %v", best)
	}
	// the profile wants 1080p, so the 720p and 2160p releases are rejected,
	// with Sonarr's reasons
	for _, r := range releases[1:] {
		if !acctest.IsBool(r["approved"], false) || len(acctest.Strs(t, r["rejections"], "rejections")) == 0 {
			t.Errorf("a release the profile does not want = %v", r)
		}
	}

	// a season: the episodes in it, from the catalogue
	season := call(t, "release_search", map[string]any{"series": breakingBad.Title, "season": 1, "limit": 2})
	if len(acctest.Rows(t, season["releases"], "releases")) != 2 || acctest.Num(t, season["total"], "total") < 7 {
		t.Errorf("season 1 = %v", season)
	}
	for _, bad := range []map[string]any{
		{"series": breakingBad.Title},
		{"series": breakingBad.Title, "episode": "S01E01", "season": 1},
		{"series": breakingBad.Title, "season": 0},
	} {
		callErr(t, "release_search", bad)
	}
	if msg := callErr(t, "release_grab", map[string]any{"guid": "nope"}); !strings.Contains(msg, "indexer_id") {
		t.Errorf("a grab with no indexer = %s", msg)
	}
}

// What Sonarr reads out of a name: the series in the library, the episodes,
// the quality, the group - and what it cannot.
func TestReleaseParse(t *testing.T) {
	out := call(t, "release_parse", map[string]any{"title": "Firefly.S01E07.Safe.720p.HDTV.x264-FAKE"})
	if !acctest.BoolOf(out["understood"]) || acctest.Str(out["series"]) != firefly.Title || acctest.NumOr0(out["series_id"]) == 0 ||
		acctest.Str(out["episodes"]) != "S01E07" || acctest.Str(out["quality"]) != "HDTV-720p" || acctest.Str(out["release_group"]) != "FAKE" ||
		!slices.Contains(acctest.Strs(t, out["languages"], "languages"), "English") {
		t.Errorf("a release name = %v", out)
	}

	// a season pack, a German release, and a multi-episode file
	pack := call(t, "release_parse", map[string]any{"title": "Breaking.Bad.S02.1080p.BluRay.x264-FAKE"})
	if !acctest.BoolOf(pack["full_season"]) || acctest.Str(pack["episodes"]) != "season 2" || acctest.Str(pack["quality"]) != "Bluray-1080p" {
		t.Errorf("a season pack = %v", pack)
	}
	german := call(t, "release_parse", map[string]any{"title": "Chernobyl.S01E05.GERMAN.1080p.WEB-DL.x264-FAKE"})
	if !slices.Contains(acctest.Strs(t, german["languages"], "languages"), "German") {
		t.Errorf("a German release = %v", german)
	}
	multi := call(t, "release_parse", map[string]any{"title": "Severance.S01E03E04.1080p.WEB-DL.x264-FAKE"})
	if acctest.Str(multi["episodes"]) != "S01E03-E04" {
		t.Errorf("a two-episode release = %v", multi)
	}

	// a show not in the library is read, but matches no series
	stranger := call(t, "release_parse", map[string]any{"title": "The.Wire.S01E01.720p.HDTV.x264-FAKE"})
	if !acctest.BoolOf(stranger["understood"]) || acctest.Str(stranger["series"]) != "" || acctest.Str(stranger["parsed_series_title"]) != "The Wire" {
		t.Errorf("a show Sonarr does not have = %v", stranger)
	}
	if junk := call(t, "release_parse", map[string]any{"title": "holiday photos"}); !acctest.IsBool(junk["understood"], false) {
		t.Errorf("a name that is no release = %v", junk)
	}
	callErr(t, "release_parse", map[string]any{"title": " "})
}

// A grab, downloading, then complete: Sonarr imports it and the episode has
// its file.
func TestDownloadImports(t *testing.T) {
	job := grabEpisode(t, breakingBad.Title, "S01E01")

	if err := sab.SetProgress(job.ID, 0.4); err != nil {
		t.Fatal(err)
	}
	row := waitQueued(t, job.ID, func(d map[string]any) bool { return acctest.NumOr0(d["progress_percent"]) == 40 })
	if acctest.Str(row["episode"]) != "S01E01" || acctest.Str(row["series"]) != breakingBad.Title || acctest.Str(row["state"]) != "downloading" ||
		acctest.Str(row["health"]) != "ok" || acctest.Str(row["quality"]) != "WEBDL-1080p" || acctest.Str(row["indexer"]) != fakeIndexerName || acctest.Str(row["time_left"]) == "" {
		t.Errorf("downloading = %v", row)
	}
	// a download going well is nothing the stuck audit wants
	if f := findings(t, call(t, "audit_stuck_downloads", nil), "subject", "S01E01"); len(f) != 0 {
		t.Errorf("a healthy download is reported stuck: %v", f)
	}
	// and it is what series_search reports queued for the series
	if q := call(t, "queue_list", map[string]any{"series": breakingBad.Title}); acctest.Num(t, q["total"], "total") < 1 {
		t.Errorf("queue_list for the series = %v", q)
	}

	completeWith(t, job, 58)
	eventually(t, "episode_list", map[string]any{"series": breakingBad.Title, "season": 1}, "S01E01 imported", func(out map[string]any) bool {
		for _, e := range acctest.RowsOf(out["episodes"]) {
			if acctest.Str(e["episode"]) == "S01E01" && acctest.BoolOf(e["has_file"]) {
				return true
			}
		}
		refreshDownloads(t)
		return false
	})
	hist := call(t, "history_list", map[string]any{"series": breakingBad.Title, "episode": "S01E01", "events": []any{"imported"}})
	imp := acctest.Rows(t, hist["entries"], "entries")
	if len(imp) == 0 || acctest.Str(imp[0]["event"]) != "downloadFolderImported" || !strings.Contains(acctest.Str(imp[0]["detail"]), "/downloads/complete/") {
		t.Errorf("the import in history = %v", hist)
	}
	if queued(t, job.ID) != nil {
		t.Error("the imported download is still queued")
	}
}

// A download the client fails: Sonarr records the failure, blocklists the
// release, and the failures audit and the blocklist tools see it.
func TestDownloadFails(t *testing.T) {
	job := grabEpisode(t, breakingBad.Title, "S01E02")
	if err := sab.Fail(job.ID, "Unpacking failed, CRC error"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "history_list", map[string]any{"series": breakingBad.Title, "events": []any{"failed"}}, "the failure", func(out map[string]any) bool {
		if len(acctest.RowsOf(out["entries"])) > 0 {
			return true
		}
		refreshDownloads(t)
		return false
	})

	failed := call(t, "audit_failed_downloads", map[string]any{"series": breakingBad.Title})
	f := only(t, failed, "subject", "S01E02")
	if acctest.Str(f["problem"]) != "download failed" || !strings.Contains(acctest.Str(f["detail"]), "CRC error") || !strings.Contains(acctest.Str(f["detail"]), "Breaking.Bad.S01E02") {
		t.Errorf("the failure = %v", f)
	}

	block := call(t, "blocklist_list", map[string]any{"series": breakingBad.Title})
	entry := findRow(t, acctest.Rows(t, block["entries"], "entries"), "source_title", job.Name)
	if acctest.Str(entry["series"]) != breakingBad.Title || acctest.Str(entry["protocol"]) != "usenet" || !strings.Contains(acctest.Str(entry["message"]), "CRC") {
		t.Errorf("the blocklist entry = %v", entry)
	}
	// blocklisted, the release is rejected by a search
	again := call(t, "release_search", map[string]any{"series": breakingBad.Title, "episode": "S01E02"})
	if rel := acctest.Rows(t, again["releases"], "releases"); len(rel) == 0 || !acctest.IsBool(rel[0]["approved"], false) || !strings.Contains(strings.Join(acctest.Strs(t, rel[0]["rejections"], "r"), " "), "locklist") {
		t.Errorf("a blocklisted release = %v", again)
	}

	removed := call(t, "blocklist_remove", map[string]any{"ids": []any{acctest.Num(t, entry["id"], "id")}})
	if acctest.Num(t, removed["removed"], "removed") != 1 {
		t.Errorf("blocklist_remove = %v", removed)
	}
	if left := call(t, "blocklist_list", map[string]any{"series": breakingBad.Title}); acctest.Num(t, left["total"], "total") != 0 {
		t.Errorf("the blocklist after removing = %v", left)
	}
	callErr(t, "blocklist_remove", map[string]any{"ids": []any{}})

	// off the blocklist, the release can be grabbed again - and fail again,
	// which is what a bad rip or a dead server looks like and what the audit
	// calls out as worth a different release. The first job leaves the
	// client first, or the second grab would find it by name
	clearDownload(t, job.ID)
	second := grabEpisode(t, breakingBad.Title, "S01E02")
	if err := sab.Fail(second.ID, "Unpacking failed, CRC error"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "audit_failed_downloads", map[string]any{"series": breakingBad.Title}, "the second failure", func(out map[string]any) bool {
		if len(findings(t, out, "problem", "repeated failures")) > 0 {
			return true
		}
		refreshDownloads(t)
		return false
	})
	twice := only(t, call(t, "audit_failed_downloads", map[string]any{"series": breakingBad.Title}), "subject", "S01E02")
	if !strings.Contains(acctest.Str(twice["detail"]), "2 failed in 30 days") || !strings.Contains(acctest.Str(twice["fix"]), "release_search") {
		t.Errorf("two failures = %v", twice)
	}
	for _, e := range acctest.Rows(t, call(t, "blocklist_list", map[string]any{"series": breakingBad.Title})["entries"], "entries") {
		call(t, "blocklist_remove", map[string]any{"ids": []any{acctest.Num(t, e["id"], "id")}})
	}
}

// A download that completes with only a sample in it: Sonarr will not import
// it, the queue says why, the stuck audit reports it, import_scan explains
// the file, and queue_remove clears it.
func TestDownloadImportBlocked(t *testing.T) {
	job := grabEpisode(t, breakingBad.Title, "S01E03")
	completeWith(t, job, 1) // a minute of an hour-long episode is a sample to Sonarr

	row := waitQueued(t, job.ID, func(d map[string]any) bool { return acctest.Str(d["health"]) != "ok" })
	if acctest.Str(row["state"]) != "importBlocked" && acctest.Str(row["state"]) != "importPending" || len(acctest.Strs(t, row["messages"], "messages")) == 0 {
		t.Fatalf("a download of a sample = %v", row)
	}
	stuck := call(t, "audit_stuck_downloads", nil)
	f := only(t, stuck, "subject", "S01E03")
	if acctest.Str(f["problem"]) != "cannot import" || !strings.Contains(acctest.Str(f["detail"]), "Sample") || !strings.Contains(acctest.Str(f["fix"]), "import_scan") {
		t.Errorf("the stuck download = %v", f)
	}

	scan := call(t, "import_scan", map[string]any{"folder": acctest.Str(row["output_path"])})
	file := acctest.Rows(t, scan["files"], "files")
	if len(file) != 1 || !acctest.IsBool(file[0]["importable"], false) || !slices.ContainsFunc(acctest.Strs(t, file[0]["rejections"], "r"), func(r string) bool { return strings.Contains(r, "ample") }) {
		t.Errorf("import_scan of the sample = %v", scan)
	}

	removed := call(t, "queue_remove", map[string]any{"ids": []any{acctest.Num(t, row["id"], "id")}, "blocklist": true, "skip_redownload": true})
	if got := acctest.Strs(t, removed["removed"], "removed"); len(got) != 1 || got[0] != acctest.Str(row["title"]) {
		t.Errorf("queue_remove = %v", removed)
	}
	if queued(t, job.ID) != nil {
		t.Error("the removed download is still queued")
	}
	// a finished download is deleted from the client's history, which
	// SABnzbd 4 does by archiving it
	if j, ok := sab.Job(job.ID); ok && !j.Archived {
		t.Error("the download is still in the client, though remove_from_client defaults to true")
	}
	if msg := callErr(t, "queue_remove", map[string]any{"ids": []any{999999}}); !strings.Contains(msg, "no download 999999") {
		t.Errorf("an unknown queue id = %s", msg)
	}
}

// A download paused in the client is one a person has to look at.
func TestDownloadPaused(t *testing.T) {
	job := grabEpisode(t, breakingBad.Title, "S01E04")
	if err := sab.Pause(job.ID); err != nil {
		t.Fatal(err)
	}
	row := waitQueued(t, job.ID, func(d map[string]any) bool { return acctest.Str(d["status"]) == "paused" })

	if f := only(t, call(t, "audit_stuck_downloads", map[string]any{"series": breakingBad.Title}), "subject", "S01E04"); acctest.Str(f["problem"]) != "paused in the download client" {
		t.Errorf("the paused download = %v", f)
	}
	// issues_only lists it, and a download going well would not be
	issues := call(t, "queue_list", map[string]any{"issues_only": true})
	if findRow(t, acctest.Rows(t, issues["downloads"], "downloads"), "download_id", acctest.Str(row["download_id"])) == nil {
		t.Error("issues_only lacks the paused download")
	}
	// queue_grab is for delayed downloads, not paused ones
	if msg := callErr(t, "queue_grab", map[string]any{"ids": []any{acctest.Num(t, row["id"], "id")}}); !strings.Contains(msg, "not held by a delay profile") {
		t.Errorf("queue_grab on a paused download = %s", msg)
	}
}

// A delay profile holds a release back; queue_grab sends it now.
func TestQueueGrabDelayed(t *testing.T) {
	skipUnlessReady(t)

	call(t, "series_edit", map[string]any{"series": breakingBad.Title, "add_tags": []any{"delayed"}})
	t.Cleanup(func() {
		call(t, "series_edit", map[string]any{"series": breakingBad.Title, "remove_tags": []any{"delayed"}})
		_, _ = invoke("tag_delete", map[string]any{"label": "delayed", "confirm": true})
	})
	tags := acctest.Rows(t, call(t, "tag_list", nil)["tags"], "tags")
	tag := acctest.Num(t, findRow(t, tags, "label", "delayed")["id"], "id")
	// a delay counts from when a release was published, and the catalogue's
	// were published two days ago, so the delay has to be longer than that
	delay, err := api.PostDelayProfile(ctx, sonarr.DelayProfileResource{
		EnableUsenet: new(true), EnableTorrent: new(true), PreferredProtocol: sonarr.DownloadProtocolUsenet,
		UsenetDelay: 7 * 24 * 60, Tags: []int{tag}, BypassIfHighestQuality: new(false),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = api.DeleteDelayProfileById(ctx, delay.Model.Id) })

	// Sonarr ignores delays for a search a person asks for, so the release
	// has to arrive the automatic way: an RSS sync, with the feed holding
	// only the one release and RSS switched on for the run
	all := indexer.Releases()
	if err := indexer.SetReleases(slices.DeleteFunc(slices.Clone(all), func(r newznab.Release) bool { return !strings.HasPrefix(r.Title, "Breaking.Bad.S01E07.") })...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := indexer.SetReleases(all...); err != nil {
			t.Error(err)
		}
	})
	setIndexerRSS(t, true)
	t.Cleanup(func() { setIndexerRSS(t, false) })
	call(t, "task_run", map[string]any{"task": "RssSync"})

	var held map[string]any
	for _, d := range acctest.Rows(t, call(t, "queue_list", map[string]any{"series": breakingBad.Title})["downloads"], "downloads") {
		if acctest.Str(d["episode"]) == "S01E07" {
			held = d
		}
	}
	if held == nil || acctest.Str(held["status"]) != "delay" {
		t.Fatalf("the delayed release = %v", held)
	}
	if f := findings(t, call(t, "audit_stuck_downloads", nil), "subject", "S01E07"); len(f) != 0 {
		t.Errorf("a download held by a delay profile is not stuck: %v", f)
	}

	out := call(t, "queue_grab", map[string]any{"ids": []any{acctest.Num(t, held["id"], "id")}})
	if got := acctest.Strs(t, out["grabbed"], "grabbed"); len(got) != 1 || !strings.Contains(got[0], "S01E07") {
		t.Errorf("queue_grab = %v", out)
	}
	job, ok := sab.FindJob("Breaking.Bad.S01E07")
	if !ok {
		t.Fatalf("queue_grab sent nothing to the client: %v", sab.Jobs())
	}
	t.Cleanup(func() { clearDownload(t, job.ID) })
	callErr(t, "queue_grab", map[string]any{"ids": []any{}})
}

// setIndexerRSS turns RSS on or off for the fake indexer. Its API key comes
// back from Sonarr masked, and Sonarr keeps the stored key when the mask is
// sent back.
func setIndexerRSS(t *testing.T, on bool) {
	t.Helper()

	res, err := api.GetIndexer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(res.Model, func(x sonarr.IndexerResource) bool { return x.Name == fakeIndexerName })
	if i < 0 {
		t.Fatal("no fake indexer")
	}
	body := res.Model[i]
	body.EnableRss = new(on)
	if _, err := api.PutIndexerById(ctx, body.Id, body, sonarr.PutIndexerByIdOperationOptions{}); err != nil {
		t.Fatal(err)
	}
}

// The search commands: each runs to the end and reports what landed in the
// queue.
func TestSearchCommands(t *testing.T) {
	skipUnlessReady(t)
	t.Cleanup(func() {
		for _, j := range sab.Jobs() {
			clearDownload(t, j.ID)
		}
	})

	ep := call(t, "episode_search", map[string]any{"series": firefly.Title, "episodes": []any{"S01E07"}})
	if acctest.Str(acctest.Object(t, ep["command"], "command")["status"]) != "completed" {
		t.Fatalf("episode_search = %v", ep)
	}
	if q := acctest.Rows(t, ep["queued"], "queued"); len(q) != 1 || acctest.Str(q[0]["episode"]) != "S01E07" {
		t.Errorf("episode_search queued = %v", ep["queued"])
	}

	season := call(t, "season_search", map[string]any{"series": firefly.Title, "season": 1})
	for _, q := range acctest.Rows(t, season["queued"], "queued") {
		if acctest.Num(t, q["season"], "season") != 1 {
			t.Errorf("season_search queued another season: %v", q)
		}
	}
	// E08 has a release in the catalogue, and E07 is already downloading
	if len(acctest.RowsOf(season["queued"])) < 2 {
		t.Errorf("season_search queued = %v", season["queued"])
	}
	callErr(t, "season_search", map[string]any{"series": firefly.Title, "season": 7})

	series := call(t, "series_search", map[string]any{"series": severance.Title})
	res := acctest.Object(t, series["result"], "result")
	if acctest.Str(acctest.Object(t, res["command"], "command")["status"]) != "completed" || acctest.Str(acctest.Object(t, res["series"], "series")["title"]) != severance.Title {
		t.Errorf("series_search = %v", series)
	}

	wanted := call(t, "wanted_search", map[string]any{"kind": "cutoff"})
	if acctest.Str(acctest.Object(t, wanted["command"], "command")["status"]) != "completed" {
		t.Errorf("wanted_search = %v", wanted)
	}
	if msg := callErr(t, "wanted_search", map[string]any{"kind": "everything"}); !strings.Contains(msg, "missing or cutoff") {
		t.Errorf("an unknown kind = %s", msg)
	}
}
