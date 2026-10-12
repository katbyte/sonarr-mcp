//go:build acceptance

package tools_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/sonarr-mcp/lib/testenv"
)

// Journeys: whole sessions the way a model drives them - the overview, the
// worklist, the fix the finding names, and the audit again to see it gone.
// Each leaves the library as it found it.

// auditCount is how many findings audit_all counts for an audit.
func auditCount(t *testing.T, name string, args map[string]any) int {
	t.Helper()

	for _, row := range acctest.Rows(t, call(t, "audit_all", args)["audits"], "audits") {
		if acctest.Str(row["audit"]) == name {
			return acctest.Num(t, row["findings"], name)
		}
	}
	t.Fatalf("audit_all has no %s", name)

	return 0
}

// From the overview to a series whose type is wrong, fixed with the tool the
// finding names.
func TestJourneyWrongSeriesType(t *testing.T) {
	before := auditCount(t, "audit_series_settings", nil)
	if before == 0 {
		t.Fatal("audit_all counts nothing wrong with series settings, and Cowboy Bebop is typed standard")
	}
	f := only(t, call(t, "audit_series_settings", nil), "problem", "anime not typed anime")
	if !strings.Contains(acctest.Str(f["fix"]), "series_edit") {
		t.Fatalf("the finding does not name its fix: %v", f)
	}

	edited := call(t, "series_edit", map[string]any{"series": acctest.Str(f["series"]), "series_type": "anime"})
	t.Cleanup(func() { call(t, "series_edit", map[string]any{"series": cowboyBebop.Title, "series_type": "standard"}) })
	if !strings.Contains(strings.Join(acctest.Strs(t, edited["changed"], "changed"), " "), "series_type: standard -> anime") {
		t.Errorf("series_edit = %v", edited)
	}

	if after := auditCount(t, "audit_series_settings", nil); after != before-1 {
		t.Errorf("audit_series_settings went from %d to %d, want one fewer", before, after)
	}
	// an anime series is searched and named by absolute number
	if got := call(t, "series_get", map[string]any{"series": cowboyBebop.Title}); acctest.Str(got["series_type"]) != "anime" {
		t.Errorf("series_get after the fix = %v", got["series_type"])
	}
}

// A missing episode, found and downloaded: the audit's gap, a search, the
// download completing, and one fewer missing.
func TestJourneyMissingEpisode(t *testing.T) {
	missing := only(t, call(t, "audit_missing_episodes", map[string]any{"series": firefly.Title}), "subject", "season 1")
	if !strings.Contains(acctest.Str(missing["detail"]), "S01E08") {
		t.Fatalf("Firefly's gap does not include E08: %v", missing)
	}

	search := call(t, "episode_search", map[string]any{"series": firefly.Title, "episodes": []any{"S01E08"}})
	queuedRows := acctest.Rows(t, search["queued"], "queued")
	if len(queuedRows) != 1 || acctest.Str(queuedRows[0]["episode"]) != "S01E08" {
		t.Fatalf("episode_search queued = %v", search["queued"])
	}
	job, ok := sab.FindJob("Firefly.S01E08")
	if !ok {
		t.Fatalf("no download for Firefly S01E08: %v", sab.Jobs())
	}
	t.Cleanup(func() {
		clearDownload(t, job.ID)
		files := call(t, "file_list", map[string]any{"series": firefly.Title, "season": 1})
		for _, f := range acctest.RowsOf(files["files"]) {
			if acctest.Str(f["episodes"]) == "S01E08" {
				call(t, "file_delete", map[string]any{"file_ids": []any{acctest.NumOr0(f["id"])}, "confirm": true})
			}
		}
	})
	completeWith(t, job, 45)
	eventually(t, "episode_list", map[string]any{"series": firefly.Title, "season": 1}, "S01E08 imported", func(out map[string]any) bool {
		for _, e := range acctest.RowsOf(out["episodes"]) {
			if acctest.Str(e["episode"]) == "S01E08" && acctest.BoolOf(e["has_file"]) {
				return true
			}
		}
		refreshDownloads(t)
		return false
	})

	after := only(t, call(t, "audit_missing_episodes", map[string]any{"series": firefly.Title}), "subject", "season 1")
	if strings.Contains(acctest.Str(after["detail"]), "S01E08") || !strings.Contains(acctest.Str(after["detail"]), "7 missing") {
		t.Errorf("after the download = %v", after)
	}
	// Sonarr named the new file to the format as it imported it
	file := findRow(t, acctest.Rows(t, call(t, "file_list", map[string]any{"series": firefly.Title, "season": 1})["files"], "files"), "episodes", "S01E08")
	if acctest.Str(file["relative_path"]) != "Season 1/Firefly - S01E08 - Ariel WEBDL-1080p.mkv" {
		t.Errorf("the imported file = %v", file["relative_path"])
	}
}

// Files deleted behind Sonarr's back: the missing files audit finds them,
// a rescan drops the records, and the episode is missing again - which the
// missing episodes audit then finds instead.
func TestJourneyFilesGoneFromDisk(t *testing.T) {
	skipUnlessReady(t)

	e02 := fireflyPath(t, 2)
	moved := hostPath(e02) + ".away"
	if err := os.Rename(hostPath(e02), moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// put it back and let Sonarr take it in again
		if err := os.Rename(moved, hostPath(e02)); err == nil {
			call(t, "series_rescan", map[string]any{"series": firefly.Title})
		}
	})

	f := only(t, call(t, "audit_missing_files", map[string]any{"series": firefly.Title}), "problem", "files gone from disk")
	if !strings.Contains(acctest.Str(f["detail"]), "S01E02") || !strings.Contains(acctest.Str(f["fix"]), "series_rescan") {
		t.Fatalf("the missing file = %v", f)
	}
	call(t, "series_rescan", map[string]any{"series": firefly.Title})

	if n := len(acctest.RowsOf(call(t, "audit_missing_files", map[string]any{"series": firefly.Title})["findings"])); n != 0 {
		t.Errorf("after the rescan %d files are still recorded but gone", n)
	}
	gap := only(t, call(t, "audit_missing_episodes", map[string]any{"series": firefly.Title}), "subject", "season 1")
	if !strings.Contains(acctest.Str(gap["detail"]), "S01E02") {
		t.Errorf("S01E02 is not missing after its file went: %v", gap)
	}
}

// Naming, from the audit to the renames: Cowboy Bebop's colon became a dash
// in the format, so its files are renamed to match.
func TestJourneyRenameToFormat(t *testing.T) {
	skipUnlessReady(t)

	f := only(t, call(t, "audit_naming", nil), "series", cowboyBebop.Title)
	if !strings.Contains(acctest.Str(f["detail"]), "Session #2 - Stray Dog Strut") {
		t.Fatalf("the rename the audit proposes = %v", f)
	}
	plan := call(t, "series_rename", map[string]any{"series": cowboyBebop.Title, "dry_run": true})
	renames := acctest.Rows(t, plan["renamed"], "renamed")
	done := call(t, "series_rename", map[string]any{"series": cowboyBebop.Title})
	if len(acctest.Rows(t, done["renamed"], "renamed")) != len(renames) || acctest.Str(acctest.Object(t, done["command"], "command")["status"]) != "completed" {
		t.Fatalf("series_rename = %v", done)
	}
	t.Cleanup(func() {
		// back to the fixture's names
		for _, r := range renames {
			_ = os.Rename(hostPath(acctest.Str(r["to"])), hostPath(acctest.Str(r["from"])))
		}
		call(t, "series_rescan", map[string]any{"series": cowboyBebop.Title})
	})
	for _, r := range renames {
		if _, err := os.Stat(hostPath(acctest.Str(r["to"]))); err != nil {
			t.Errorf("not renamed to %s: %v", r["to"], err)
		}
	}
	if left := findings(t, call(t, "audit_naming", nil), "series", cowboyBebop.Title); len(left) != 0 {
		t.Errorf("after the rename = %v", left)
	}
	if _, err := os.Stat(filepath.Join(tvDir(), cowboyBebop.Folder)); err != nil {
		t.Error(err)
	}
}

// A continuing series set to ignore new seasons: the monitoring audit, and
// the setting it names.
func TestJourneyMonitorNewSeasons(t *testing.T) {
	f := only(t, call(t, "audit_monitoring", nil), "series", severance.Title)
	if !strings.Contains(acctest.Str(f["fix"]), "monitor_new_items all") {
		t.Fatalf("the finding = %v", f)
	}

	call(t, "series_edit", map[string]any{"series": severance.Title, "monitor_new_items": "all"})
	t.Cleanup(func() { call(t, "series_edit", map[string]any{"series": severance.Title, "monitor_new_items": "none"}) })

	if left := findings(t, call(t, "audit_monitoring", nil), "series", severance.Title); len(left) != 0 {
		t.Errorf("after the fix = %v", left)
	}
	// the other gap the audit knows: the latest season left unmonitored
	call(t, "season_monitor", map[string]any{"series": severance.Title, "seasons": []any{2}, "monitored": false})
	t.Cleanup(func() {
		call(t, "season_monitor", map[string]any{"series": severance.Title, "seasons": []any{2}, "monitored": true})
	})
	latest := findings(t, call(t, "audit_monitoring", nil), "series", severance.Title)
	if len(latest) != 1 || acctest.Str(latest[0]["problem"]) != "latest season not monitored" {
		// Severance may have a third season listed by now, in which case the
		// latest is not the one unmonitored here
		if len(latest) != 0 || !hasSeason(t, severance.Title, 3) {
			t.Errorf("with season 2 unmonitored = %v", latest)
		}
	}
}

// hasSeason reports whether a series lists a season.
func hasSeason(t *testing.T, series string, season int) bool {
	t.Helper()

	for _, s := range acctest.Rows(t, call(t, "series_get", map[string]any{"series": series})["seasons"], "seasons") {
		if acctest.Num(t, s["season"], "season") == season {
			return true
		}
	}

	return false
}

// A folder renamed outside Sonarr: the series has lost its folder, the
// folder belongs to no series, and both audits name the same fix - point the
// series at it, rather than add the show a second time.
func TestJourneyFolderRenamed(t *testing.T) {
	skipUnlessReady(t)

	from := filepath.Join(tvDir(), cowboyBebop.Folder)
	renamed := cowboyBebop.Title + " (" + itoa(cowboyBebop.Year) + ")"
	to := filepath.Join(tvDir(), renamed)
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			_ = os.Rename(to, from)
		}
		call(t, "series_edit", map[string]any{"series": cowboyBebop.Title, "path": "/tv/" + cowboyBebop.Folder})
		call(t, "series_rescan", map[string]any{"series": cowboyBebop.Title})
	})

	f := only(t, call(t, "audit_missing_folders", nil), "series", cowboyBebop.Title)
	if acctest.Str(f["problem"]) != "series folder renamed" || !strings.Contains(acctest.Str(f["fix"]), `series_edit path "/tv/`+renamed+`"`) {
		t.Fatalf("the renamed folder = %v", f)
	}
	// and from the other side, without offering to add the show again
	u := only(t, call(t, "audit_unmapped_folders", nil), "subject", "/tv/"+renamed)
	if acctest.Str(u["problem"]) != "folder of a series that moved" || !strings.Contains(acctest.Str(u["fix"]), "series_edit "+cowboyBebop.Title) {
		t.Fatalf("the folder left behind = %v", u)
	}

	call(t, "series_edit", map[string]any{"series": cowboyBebop.Title, "path": "/tv/" + renamed})
	call(t, "series_rescan", map[string]any{"series": cowboyBebop.Title})

	// Sonarr holds the series where it is now, with its files
	got := call(t, "series_get", map[string]any{"series": cowboyBebop.Title})
	if acctest.Str(got["path"]) != "/tv/"+renamed || acctest.NumOr0(got["episodes_have"]) != cowboyBebop.Files {
		t.Errorf("after the move = %v at %v", got["episodes_have"], got["path"])
	}
	if gone := findings(t, call(t, "audit_missing_files", nil), "series", cowboyBebop.Title); len(gone) != 0 {
		t.Errorf("files recorded where they are not: %v", gone)
	}
	if left := findings(t, call(t, "audit_missing_folders", nil), "series", cowboyBebop.Title); len(left) != 0 {
		t.Errorf("after the fix = %v", left)
	}
	if left := findings(t, call(t, "audit_unmapped_folders", nil), "subject", "/tv/"+renamed); len(left) != 0 {
		t.Errorf("the folder is still unknown to Sonarr: %v", left)
	}

	// put the library back the way the rest of the suite expects it
	if err := os.Rename(to, from); err != nil {
		t.Fatal(err)
	}
	restored = true
}

// hasFile waits for an episode of Breaking Bad's first season to have a
// file, having Sonarr look at its download clients as it waits.
func hasFile(t *testing.T, episode string) {
	t.Helper()

	for range 60 {
		out := call(t, "episode_list", map[string]any{"series": breakingBad.Title, "season": 1})
		for _, e := range acctest.RowsOf(out["episodes"]) {
			if acctest.Str(e["episode"]) == episode && acctest.BoolOf(e["has_file"]) {
				return
			}
		}
		refreshDownloads(t)
		time.Sleep(time.Second)
	}
	// what Sonarr holds of it says why it did not come in
	queue, _ := invoke("queue_list", map[string]any{"series": breakingBad.Title})
	log, _ := invoke("log_list", map[string]any{"level": "warn", "limit": 5})
	t.Fatalf("%s never had a file. The queue: %v\nthe log: %v", episode, queue["downloads"], log["entries"])
}

// dropFiles deletes the files Breaking Bad has for an episode, so a journey
// leaves the series with none, as it found it.
func dropFiles(episode string) {
	files, err := invoke("file_list", map[string]any{"series": breakingBad.Title, "season": 1})
	if err != nil {
		return
	}
	for _, f := range acctest.RowsOf(files["files"]) {
		if acctest.Str(f["episodes"]) == episode {
			_, _ = invoke("file_delete", map[string]any{"file_ids": []any{acctest.NumOr0(f["id"])}, "confirm": true})
		}
	}
}

// A download Sonarr cannot import, dealt with the way its finding says: it is
// taken out of the queue and blocklisted, the episode is searched for again,
// and a different release comes in from a second indexer the journey adds.
func TestJourneyStuckDownloadReplaced(t *testing.T) {
	const episode, other = "S01E04", "Alternate Indexer"

	// (the other release is not called -OTHER: Sonarr takes a file ending
	// -other, -trailer or -sample for an extra, and passes over it)
	second, err := fakes.AddIndexer("alternate", release("Breaking.Bad.S01E04.Cancer.Man.1080p.WEB-DL.DD5.1.H.264-ALT", breakingBad.TvdbID, 1, 4, 1.5, newznab.CategoryTVHD))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, j := range sab.Jobs() {
			if strings.Contains(j.Name, episode) {
				clearDownload(t, j.ID)
			}
		}
		dropFiles(episode)
		if list, listErr := invoke("blocklist_list", nil); listErr == nil {
			for _, b := range acctest.RowsOf(list["entries"]) {
				if strings.Contains(acctest.Str(b["source_title"]), episode) {
					_, _ = invoke("blocklist_remove", map[string]any{"ids": []any{acctest.NumOr0(b["id"])}})
				}
			}
		}
		_, _ = invoke("indexer_delete", map[string]any{"which": other, "confirm": true})
	})
	call(t, "indexer_add", map[string]any{
		"kind": "Newznab", "name": other, "set": map[string]any{"enableRss": false, "priority": 40},
		"settings": map[string]any{"baseUrl": second.URL(), "apiPath": "/api", "apiKey": testenv.FakeKey, "categories": []any{5030, 5040}},
	})

	// both indexers have the episode at the quality the profile wants; the
	// first one's release is the one grabbed, and it comes down as a sample
	found := acctest.Rows(t, call(t, "release_search", map[string]any{"series": breakingBad.Title, "episode": episode})["releases"], "releases")
	first := found[slices.IndexFunc(found, func(r map[string]any) bool { return strings.HasSuffix(acctest.Str(r["title"]), "-FAKE") })]
	if !acctest.BoolOf(first["approved"]) || len(found) != 2 {
		t.Fatalf("the releases for %s = %v", episode, found)
	}
	call(t, "release_grab", map[string]any{"guid": acctest.Str(first["guid"]), "indexer_id": acctest.Num(t, first["indexer_id"], "indexer_id")})
	job, ok := sab.FindJob("H.264-FAKE")
	for _, j := range sab.Jobs() {
		if strings.Contains(j.Name, episode) && strings.HasSuffix(j.Name, "-FAKE") {
			job, ok = j, true
		}
	}
	if !ok {
		t.Fatalf("the fake client has no job for %s: %v", episode, sab.Jobs())
	}
	completeWith(t, job, 1)
	row := waitQueued(t, job.ID, func(d map[string]any) bool { return acctest.Str(d["health"]) != "ok" })

	// the audit finds it and names the fix, which is taken
	stuck := only(t, call(t, "audit_stuck_downloads", nil), "subject", episode)
	if !strings.Contains(acctest.Str(stuck["fix"]), "queue_remove") {
		t.Fatalf("the stuck download = %v", stuck)
	}
	call(t, "queue_remove", map[string]any{"ids": []any{acctest.Num(t, row["id"], "id")}, "blocklist": true, "skip_redownload": true})
	blocked := acctest.Rows(t, call(t, "blocklist_list", nil)["entries"], "entries")
	if !slices.ContainsFunc(blocked, func(b map[string]any) bool {
		return strings.HasSuffix(acctest.Str(b["source_title"]), "S01E04.Cancer.Man.1080p.WEB-DL.DD5.1.H.264-FAKE")
	}) {
		t.Fatalf("the release is not blocklisted: %v", blocked)
	}

	// searched for again, Sonarr passes over the one it was told to refuse
	// and takes the other indexer's
	search := call(t, "episode_search", map[string]any{"series": breakingBad.Title, "episodes": []any{episode}})
	if q := acctest.Rows(t, search["queued"], "queued"); len(q) != 1 || !strings.HasSuffix(acctest.Str(q[0]["title"]), "-ALT") || acctest.Str(q[0]["indexer"]) != other {
		t.Fatalf("what the second search queued = %v", search["queued"])
	}
	replacement, ok := sab.FindJob("H.264-ALT")
	if !ok {
		t.Fatalf("the fake client has no job for the other release: %v", sab.Jobs())
	}
	completeWith(t, replacement, 48)
	hasFile(t, episode)

	file := findRow(t, acctest.Rows(t, call(t, "file_list", map[string]any{"series": breakingBad.Title, "season": 1})["files"], "files"), "episodes", episode)
	if acctest.Str(file["release_group"]) != "ALT" || acctest.Str(file["quality"]) != "WEBDL-1080p" {
		t.Errorf("the file that came in = %v", file)
	}
	if left := findings(t, call(t, "audit_stuck_downloads", nil), "subject", episode); len(left) != 0 {
		t.Errorf("the download is still reported stuck: %v", left)
	}
}

// A file below its profile's cutoff, upgraded: the audit names the episode,
// a search brings a better release in, and Sonarr replaces the file with it.
func TestJourneyQualityUpgrade(t *testing.T) {
	const episode = "S01E06"

	// the series was added with no files, and its folder is where Sonarr put it
	folder := strings.TrimPrefix(acctest.Str(call(t, "series_get", map[string]any{"series": breakingBad.Title})["path"]), "/tv/")
	low := filepath.Join(tvDir(), folder, "Season 1", "Breaking Bad - S01E06 - Crazy Handful of Nothin SDTV.mkv")
	if folder == "" {
		t.Fatal("Breaking Bad has no folder under /tv")
	}
	if err := fakeVideo(low, 48, "640x360"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if job, ok := sab.FindJob("Breaking.Bad." + episode); ok {
			clearDownload(t, job.ID)
		}
		dropFiles(episode)
		_ = os.Remove(low)
		_, _ = invoke("series_rescan", map[string]any{"series": breakingBad.Title})
	})
	call(t, "series_rescan", map[string]any{"series": breakingBad.Title})
	had := findRow(t, acctest.Rows(t, call(t, "file_list", map[string]any{"series": breakingBad.Title, "season": 1})["files"], "files"), "episodes", episode)
	if acctest.Str(had["quality"]) != "SDTV" || !acctest.BoolOf(had["below_cutoff"]) {
		t.Fatalf("the file to upgrade = %v", had)
	}

	// the audit names the file, and what stands in the way of replacing it:
	// the profile does not upgrade, so a search brings nothing in
	below := only(t, call(t, "audit_cutoff_unmet", map[string]any{"series": breakingBad.Title}), "series", breakingBad.Title)
	if !strings.Contains(acctest.Str(below["detail"]), episode+" SDTV") || !strings.Contains(acctest.Str(below["detail"]), "Upgrades Allowed off") || !strings.Contains(acctest.Str(below["fix"]), "profile_edit") || !strings.Contains(acctest.Str(below["fix"]), "episode_search") {
		t.Fatalf("the file below cutoff = %v", below)
	}
	if early := call(t, "episode_search", map[string]any{"series": breakingBad.Title, "episodes": []any{episode}}); len(acctest.RowsOf(early["queued"])) != 0 {
		t.Fatalf("a search queued %v though the profile does not upgrade", early["queued"])
	}
	// the fix it names is taken, and put back when the journey is over
	t.Cleanup(func() { _, _ = invoke("profile_edit", map[string]any{"profile": profile, "upgrade_allowed": false}) })
	if allowed := call(t, "profile_edit", map[string]any{"profile": profile, "upgrade_allowed": true}); !slices.Contains(acctest.Strs(t, allowed["changed"], "changed"), "upgrade_allowed: false -> true") {
		t.Fatalf("profile_edit = %v", allowed)
	}

	search := call(t, "episode_search", map[string]any{"series": breakingBad.Title, "episodes": []any{episode}})
	if q := acctest.Rows(t, search["queued"], "queued"); len(q) != 1 || acctest.Str(q[0]["quality"]) != "WEBDL-1080p" {
		t.Fatalf("what the search queued = %v", search["queued"])
	}
	job, ok := sab.FindJob("Breaking.Bad." + episode)
	if !ok {
		t.Fatalf("the fake client has no job for %s: %v", episode, sab.Jobs())
	}
	completeWith(t, job, 48)

	// the better file takes the place of the one that was there
	eventually(t, "file_list", map[string]any{"series": breakingBad.Title, "season": 1}, "the upgrade imported", func(out map[string]any) bool {
		for _, f := range acctest.RowsOf(out["files"]) {
			if acctest.Str(f["episodes"]) == episode && acctest.Str(f["quality"]) == "WEBDL-1080p" {
				return true
			}
		}
		refreshDownloads(t)
		return false
	})
	files := acctest.Rows(t, call(t, "file_list", map[string]any{"series": breakingBad.Title, "season": 1})["files"], "files")
	if n := len(findAll(files, "episodes", episode)); n != 1 {
		t.Errorf("%s has %d files after the upgrade, want the one that replaced the other", episode, n)
	}
	if now := findRow(t, files, "episodes", episode); !acctest.IsBool(now["below_cutoff"], false) {
		t.Errorf("the file after the upgrade = %v", now)
	}
	if _, err := os.Stat(low); !os.IsNotExist(err) {
		t.Errorf("the file that was upgraded is still on disk: %v", err)
	}
	if left := findings(t, call(t, "audit_cutoff_unmet", map[string]any{"series": breakingBad.Title}), "series", breakingBad.Title); len(left) != 0 {
		t.Errorf("the series is still below cutoff: %v", left)
	}
}

// findAll are the rows with a field of a value.
func findAll(rows []map[string]any, field, value string) []map[string]any {
	var out []map[string]any
	for _, row := range rows {
		if acctest.Str(row[field]) == value {
			out = append(out, row)
		}
	}

	return out
}

// The torrent path, end to end, with no usenet in it: a Torznab indexer and a
// torrent blackhole added with the tools, a release found on the one and
// grabbed, its .torrent handed to the other, and what that "downloads"
// imported.
func TestJourneyTorrentDownload(t *testing.T) {
	const episode, tracker, client = "S01E07", "Torrent Indexer", "Torrent Blackhole"
	const title = "Breaking.Bad.S01E07.A.No.Rough.Stuff.Type.Deal.1080p.WEB-DL.DD5.1.H.264-TORRENT"

	rel := release(title, breakingBad.TvdbID, 1, 7, 1.4, newznab.CategoryTVHD)
	rel.Seeders, rel.Peers = 25, 30
	torrents, err := fakes.AddTorrentIndexer("torrents", rel)
	if err != nil {
		t.Fatal(err)
	}
	inbox, watch := testenv.DataDir("downloads", "torrents"), testenv.DataDir("downloads", "watch")
	for _, dir := range []string{inbox, watch} {
		if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container writes to it as another user
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // the same
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dropFiles(episode)
		_, _ = invoke("downloadclient_delete", map[string]any{"which": client, "confirm": true})
		_, _ = invoke("indexer_delete", map[string]any{"which": tracker, "confirm": true})
		_ = os.RemoveAll(inbox)
		_ = os.RemoveAll(watch)
	})

	call(t, "indexer_add", map[string]any{
		"kind": "Torznab", "name": tracker, "set": map[string]any{"enableRss": false},
		"settings": map[string]any{"baseUrl": torrents.URL(), "apiPath": "/api", "apiKey": testenv.FakeKey, "categories": []any{5030, 5040}},
	})
	if rec := record(t, call(t, "downloadclient_add", map[string]any{"kind": "TorrentBlackhole", "name": client, "settings": map[string]any{"torrentFolder": "/downloads/torrents", "watchFolder": "/downloads/watch"}})); acctest.Str(rec["protocol"]) != "torrent" {
		t.Fatalf("the blackhole added = %v", rec)
	}

	found := acctest.Rows(t, call(t, "release_search", map[string]any{"series": breakingBad.Title, "episode": episode})["releases"], "releases")
	torrent := found[slices.IndexFunc(found, func(r map[string]any) bool { return acctest.Str(r["protocol"]) == "torrent" })]
	if acctest.Str(torrent["title"]) != title || acctest.Str(torrent["indexer"]) != tracker || acctest.NumOr0(torrent["seeders"]) != 25 || !acctest.BoolOf(torrent["approved"]) {
		t.Fatalf("the torrent release = %v", torrent)
	}
	grab := call(t, "release_grab", map[string]any{"guid": acctest.Str(torrent["guid"]), "indexer_id": acctest.Num(t, torrent["indexer_id"], "indexer_id")})
	if g := acctest.Object(t, grab["grabbed"], "grabbed"); acctest.Str(g["download_client"]) != client || acctest.Str(g["episode"]) != episode {
		t.Fatalf("release_grab of the torrent = %v", grab)
	}
	// the .torrent the indexer handed over is in the folder the client reads
	if dropped, _ := filepath.Glob(filepath.Join(inbox, "*.torrent")); len(dropped) != 1 {
		t.Fatalf("%d .torrent files were handed to the blackhole, want one", len(dropped))
	}

	// what a torrent client would leave where Sonarr watches for it
	if err := fakeVideo(filepath.Join(watch, title, title+".mkv"), 48, "1920x1080"); err != nil {
		t.Fatal(err)
	}
	hasFile(t, episode)
	file := findRow(t, acctest.Rows(t, call(t, "file_list", map[string]any{"series": breakingBad.Title, "season": 1})["files"], "files"), "episodes", episode)
	if acctest.Str(file["release_group"]) != "TORRENT" || acctest.Str(file["quality"]) != "WEBDL-1080p" {
		t.Errorf("the file that came in = %v", file)
	}
	hist := acctest.Rows(t, call(t, "history_list", map[string]any{"series": breakingBad.Title, "episode": episode, "events": []any{"imported"}})["entries"], "entries")
	if len(hist) == 0 || !strings.Contains(acctest.Str(hist[0]["detail"]), "/downloads/watch/") {
		t.Errorf("the import in history = %v", hist)
	}
}
