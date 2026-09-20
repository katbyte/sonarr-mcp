//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Journeys: whole sessions the way a model drives them - the overview, the
// worklist, the fix the finding names, and the audit again to see it gone.
// Each leaves the library as it found it.

// auditCount is how many findings audit_all counts for an audit.
func auditCount(t *testing.T, name string, args map[string]any) int {
	t.Helper()

	for _, row := range rows(t, call(t, "audit_all", args)["audits"], "audits") {
		if str(row["audit"]) == name {
			return num(t, row["findings"], name)
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
	if !strings.Contains(str(f["fix"]), "series_edit") {
		t.Fatalf("the finding does not name its fix: %v", f)
	}

	edited := call(t, "series_edit", map[string]any{"series": str(f["series"]), "series_type": "anime"})
	t.Cleanup(func() { call(t, "series_edit", map[string]any{"series": cowboyBebop.Title, "series_type": "standard"}) })
	if !strings.Contains(strings.Join(strs(t, edited["changed"], "changed"), " "), "series_type: standard -> anime") {
		t.Errorf("series_edit = %v", edited)
	}

	if after := auditCount(t, "audit_series_settings", nil); after != before-1 {
		t.Errorf("audit_series_settings went from %d to %d, want one fewer", before, after)
	}
	// an anime series is searched and named by absolute number
	if got := call(t, "series_get", map[string]any{"series": cowboyBebop.Title}); str(got["series_type"]) != "anime" {
		t.Errorf("series_get after the fix = %v", got["series_type"])
	}
}

// A missing episode, found and downloaded: the audit's gap, a search, the
// download completing, and one fewer missing.
func TestJourneyMissingEpisode(t *testing.T) {
	missing := only(t, call(t, "audit_missing_episodes", map[string]any{"series": firefly.Title}), "subject", "season 1")
	if !strings.Contains(str(missing["detail"]), "S01E08") {
		t.Fatalf("Firefly's gap does not include E08: %v", missing)
	}

	search := call(t, "episode_search", map[string]any{"series": firefly.Title, "episodes": []any{"S01E08"}})
	queuedRows := rows(t, search["queued"], "queued")
	if len(queuedRows) != 1 || str(queuedRows[0]["episode"]) != "S01E08" {
		t.Fatalf("episode_search queued = %v", search["queued"])
	}
	job, ok := sab.FindJob("Firefly.S01E08")
	if !ok {
		t.Fatalf("no download for Firefly S01E08: %v", sab.Jobs())
	}
	t.Cleanup(func() {
		clearDownload(t, job.ID)
		files := call(t, "file_list", map[string]any{"series": firefly.Title, "season": 1})
		for _, f := range rowsOf(files["files"]) {
			if str(f["episodes"]) == "S01E08" {
				call(t, "file_delete", map[string]any{"file_ids": []any{numOr0(f["id"])}})
			}
		}
	})
	completeWith(t, job, 45)
	eventually(t, "episode_list", map[string]any{"series": firefly.Title, "season": 1}, "S01E08 imported", func(out map[string]any) bool {
		for _, e := range rowsOf(out["episodes"]) {
			if str(e["episode"]) == "S01E08" && e["has_file"] == true {
				return true
			}
		}
		refreshDownloads(t)
		return false
	})

	after := only(t, call(t, "audit_missing_episodes", map[string]any{"series": firefly.Title}), "subject", "season 1")
	if strings.Contains(str(after["detail"]), "S01E08") || !strings.Contains(str(after["detail"]), "7 missing") {
		t.Errorf("after the download = %v", after)
	}
	// Sonarr named the new file to the format as it imported it
	file := findRow(t, rows(t, call(t, "file_list", map[string]any{"series": firefly.Title, "season": 1})["files"], "files"), "episodes", "S01E08")
	if str(file["relative_path"]) != "Season 1/Firefly - S01E08 - Ariel WEBDL-1080p.mkv" {
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
	if !strings.Contains(str(f["detail"]), "S01E02") || !strings.Contains(str(f["fix"]), "series_rescan") {
		t.Fatalf("the missing file = %v", f)
	}
	call(t, "series_rescan", map[string]any{"series": firefly.Title})

	if n := len(rowsOf(call(t, "audit_missing_files", map[string]any{"series": firefly.Title})["findings"])); n != 0 {
		t.Errorf("after the rescan %d files are still recorded but gone", n)
	}
	gap := only(t, call(t, "audit_missing_episodes", map[string]any{"series": firefly.Title}), "subject", "season 1")
	if !strings.Contains(str(gap["detail"]), "S01E02") {
		t.Errorf("S01E02 is not missing after its file went: %v", gap)
	}
}

// Naming, from the audit to the renames: Cowboy Bebop's colon became a dash
// in the format, so its files are renamed to match.
func TestJourneyRenameToFormat(t *testing.T) {
	skipUnlessReady(t)

	f := only(t, call(t, "audit_naming", nil), "series", cowboyBebop.Title)
	if !strings.Contains(str(f["detail"]), "Session #2 - Stray Dog Strut") {
		t.Fatalf("the rename the audit proposes = %v", f)
	}
	plan := call(t, "series_rename", map[string]any{"series": cowboyBebop.Title, "dry_run": true})
	renames := rows(t, plan["renamed"], "renamed")
	done := call(t, "series_rename", map[string]any{"series": cowboyBebop.Title})
	if len(rows(t, done["renamed"], "renamed")) != len(renames) || str(object(t, done["command"], "command")["status"]) != "completed" {
		t.Fatalf("series_rename = %v", done)
	}
	t.Cleanup(func() {
		// back to the fixture's names
		for _, r := range renames {
			_ = os.Rename(hostPath(str(r["to"])), hostPath(str(r["from"])))
		}
		call(t, "series_rescan", map[string]any{"series": cowboyBebop.Title})
	})
	for _, r := range renames {
		if _, err := os.Stat(hostPath(str(r["to"]))); err != nil {
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
	if !strings.Contains(str(f["fix"]), "monitor_new_items all") {
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
	if len(latest) != 1 || str(latest[0]["problem"]) != "latest season not monitored" {
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

	for _, s := range rows(t, call(t, "series_get", map[string]any{"series": series})["seasons"], "seasons") {
		if num(t, s["season"], "season") == season {
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
	if str(f["problem"]) != "series folder renamed" || !strings.Contains(str(f["fix"]), `series_edit path "/tv/`+renamed+`"`) {
		t.Fatalf("the renamed folder = %v", f)
	}
	// and from the other side, without offering to add the show again
	u := only(t, call(t, "audit_unmapped_folders", nil), "subject", "/tv/"+renamed)
	if str(u["problem"]) != "folder of a series that moved" || !strings.Contains(str(u["fix"]), "series_edit "+cowboyBebop.Title) {
		t.Fatalf("the folder left behind = %v", u)
	}

	call(t, "series_edit", map[string]any{"series": cowboyBebop.Title, "path": "/tv/" + renamed})
	call(t, "series_rescan", map[string]any{"series": cowboyBebop.Title})

	// Sonarr holds the series where it is now, with its files
	got := call(t, "series_get", map[string]any{"series": cowboyBebop.Title})
	if str(got["path"]) != "/tv/"+renamed || numOr0(got["episodes_have"]) != cowboyBebop.Files {
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
