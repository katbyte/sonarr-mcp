//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Importing what Sonarr has lost track of: files dropped into a series
// folder, a file whose name says nothing, and a whole folder no series lives
// in.

func TestImportScanAndApply(t *testing.T) {
	skipUnlessReady(t)

	season := filepath.Join(tvDir(), firefly.Folder, "Season 1")
	named := filepath.Join(season, "Firefly.S01E09.War.Stories.1080p.WEB-DL.DD5.1.H.264-FAKE.mkv")
	odd := filepath.Join(season, "war stories extended.mkv")
	for _, p := range []string{named, odd} {
		if err := fakeVideo(p, 45, "1920x1080"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		// whatever import_apply did with them, the episodes go back to missing
		out, err := invoke("file_list", map[string]any{"series": firefly.Title, "season": 1})
		if err != nil {
			return
		}
		for _, f := range rowsOf(out["files"]) {
			if e := str(f["episodes"]); e == "S01E09" || e == "S01E10" {
				_, _ = invoke("file_delete", map[string]any{"file_ids": []any{numOr0(f["id"])}})
			}
		}
		_ = os.Remove(named)
		_ = os.Remove(odd)
	})

	// scanning the series' folder finds the two untracked files, and only them
	scan := call(t, "import_scan", map[string]any{"series": firefly.Title})
	files := rows(t, scan["files"], "files")
	if str(scan["folder"]) != "/tv/"+firefly.Folder || len(files) != 2 {
		t.Fatalf("import_scan = %v", scan)
	}
	ready := findRow(t, files, "path", "/tv/"+firefly.Folder+"/Season 1/Firefly.S01E09.War.Stories.1080p.WEB-DL.DD5.1.H.264-FAKE.mkv")
	if ready["importable"] != true || str(ready["episodes"]) != "S01E09" || str(ready["series"]) != firefly.Title || str(ready["quality"]) != "WEBDL-1080p" {
		t.Errorf("the named file = %v", ready)
	}
	unknown := findRow(t, files, "path", "/tv/"+firefly.Folder+"/Season 1/war stories extended.mkv")
	if unknown["importable"] != false || str(unknown["episodes"]) != "" {
		t.Errorf("the unnamed file = %v", unknown)
	}
	// the same by folder
	if byFolder := call(t, "import_scan", map[string]any{"folder": "/tv/" + firefly.Folder + "/Season 1"}); len(rowsOf(byFolder["files"])) != 2 {
		t.Errorf("import_scan by folder = %v", byFolder)
	}

	// the named file imports on what Sonarr read; the other needs its
	// episode given, and it is imported as S01E10
	out := call(t, "import_apply", map[string]any{"files": []any{
		map[string]any{"path": str(ready["path"])},
		map[string]any{"path": str(unknown["path"]), "series": firefly.Title, "episodes": []any{"S01E10"}, "quality": "WEBDL-1080p"},
	}})
	applied := rows(t, out["files"], "files")
	if str(object(t, out["command"], "command")["status"]) != "completed" || len(applied) != 2 {
		t.Fatalf("import_apply = %v", out)
	}
	for _, f := range applied {
		if f["imported"] != true {
			t.Errorf("not imported: %v", f)
		}
	}
	eps := rows(t, call(t, "episode_list", map[string]any{"series": firefly.Title, "season": 1})["episodes"], "episodes")
	for _, want := range []string{"S01E09", "S01E10"} {
		if e := findRow(t, eps, "episode", want); e["has_file"] != true {
			t.Errorf("%s has no file after the import: %v", want, e)
		}
	}
	// a file already in the series folder is taken in where it is, and no
	// longer shows as untracked
	if _, err := os.Stat(named); err != nil {
		t.Errorf("the imported file moved: %v", err)
	}
	if left := call(t, "import_scan", map[string]any{"series": firefly.Title}); len(rowsOf(left["files"])) != 0 {
		t.Errorf("import_scan after the import = %v", left)
	}

	// what cannot be done says why
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"files": []any{}}, "name the files"},
		{map[string]any{"files": []any{map[string]any{"path": "/tv/nowhere.mkv"}}}, "no importable video file"},
		{map[string]any{"files": []any{map[string]any{"path": str(ready["path"])}}, "mode": "hardlink"}, "mode must be"},
	} {
		if msg := callErr(t, "import_apply", c.args); !strings.Contains(msg, c.want) {
			t.Errorf("import_apply %v = %s, want %q", c.args, msg, c.want)
		}
	}
	callErr(t, "import_scan", nil)
	callErr(t, "import_scan", map[string]any{"folder": "/tv", "series": firefly.Title})
}

// The Expanse's folder is in /tv with nothing in Sonarr for it: series_import
// matches it and takes it in, files and all.
func TestSeriesImport(t *testing.T) {
	skipUnlessReady(t)

	plan := call(t, "series_import", map[string]any{"folders": []any{theExpanse.Folder, "No Such Folder"}, "dry_run": true})
	rowsOut := rows(t, plan["folders"], "folders")
	expanse := findRow(t, rowsOut, "folder", theExpanse.Folder)
	if str(expanse["status"]) != "would import" || num(t, object(t, expanse["series"], "series")["tvdb_id"], "tvdb") != theExpanse.TvdbID {
		t.Fatalf("the dry run = %v", expanse)
	}
	if nope := findRow(t, rowsOut, "folder", "No Such Folder"); str(nope["status"]) != "no match" {
		t.Errorf("an unknown folder = %v", nope)
	}
	// a dry run imports nothing
	if msg := callErr(t, "series_get", map[string]any{"series": theExpanse.Title}); !strings.Contains(msg, "no series") {
		t.Fatalf("the dry run imported it: %s", msg)
	}

	out := call(t, "series_import", map[string]any{
		"folders": []any{theExpanse.Folder}, "quality_profile": profile, "monitor": "existing",
		"matches": map[string]any{theExpanse.Folder: "tvdb:" + itoa(theExpanse.TvdbID)},
	})
	if r := rows(t, out["folders"], "folders"); len(r) != 1 || str(r[0]["status"]) != "imported" {
		t.Fatalf("series_import = %v", out)
	}
	t.Cleanup(func() {
		// back to an unmapped folder, its files kept, for the audit fixture
		_, _ = invoke("series_delete", map[string]any{"series": theExpanse.Title})
	})
	if err := waitForFiles(theExpanse.Title, theExpanse.Files); err != nil {
		t.Fatal(err)
	}
	got := call(t, "series_get", map[string]any{"series": theExpanse.Title})
	if str(got["path"]) != "/tv/"+theExpanse.Folder || str(got["quality_profile"]) != profile {
		t.Errorf("the imported series = %v", got)
	}
	// monitor existing: only the episodes with files are wanted
	if missing := call(t, "episode_list", map[string]any{"series": theExpanse.Title, "missing": true}); num(t, missing["total"], "total") != 0 {
		t.Errorf("monitor existing left %v episodes missing", missing["total"])
	}
	// and the folder is no longer unmapped
	if f := findings(t, call(t, "audit_unmapped_folders", nil), "subject", "/tv/"+theExpanse.Folder); len(f) != 0 {
		t.Errorf("still unmapped: %v", f)
	}
	// importing it again reports it as in the library already, now it is
	// not an unmapped folder
	if again := call(t, "series_import", map[string]any{"folders": []any{theExpanse.Folder}}); str(rows(t, again["folders"], "f")[0]["status"]) != "no match" {
		t.Errorf("importing it again = %v", again)
	}
	if _, err := os.Stat(filepath.Join(tvDir(), theExpanse.Folder)); err != nil {
		t.Errorf("the folder is gone: %v", err)
	}
}

// history_mark_failed on a grab: the release is blocklisted and the history
// records the failure, for a download that finished but was bad.
func TestHistoryMarkFailed(t *testing.T) {
	job := grabEpisode(t, breakingBad.Title, "S01E06")
	hist := call(t, "history_list", map[string]any{"series": breakingBad.Title, "episode": "S01E06", "events": []any{"grabbed"}})
	grabs := rows(t, hist["entries"], "entries")
	if len(grabs) == 0 || str(grabs[0]["source_title"]) != job.Name {
		t.Fatalf("the grab in history = %v", hist)
	}

	out := call(t, "history_mark_failed", map[string]any{"history_id": num(t, grabs[0]["id"], "id")})
	if f := object(t, out["marked_failed"], "marked_failed"); str(f["source_title"]) != job.Name || str(f["event"]) != "grabbed" {
		t.Errorf("history_mark_failed = %v", out)
	}
	eventually(t, "blocklist_list", map[string]any{"series": breakingBad.Title}, "the blocklisted grab", func(out map[string]any) bool {
		for _, e := range rowsOf(out["entries"]) {
			if str(e["source_title"]) == job.Name {
				return true
			}
		}
		return false
	})
	failed := call(t, "history_list", map[string]any{"series": breakingBad.Title, "episode": "S01E06", "events": []any{"failed"}, "days": 1})
	if e := rows(t, failed["entries"], "entries"); len(e) == 0 || !strings.Contains(str(e[0]["detail"]), "Manually marked as failed") {
		t.Errorf("the failure in history = %v", failed)
	}
	t.Cleanup(func() {
		block := call(t, "blocklist_list", map[string]any{"series": breakingBad.Title})
		for _, e := range rowsOf(block["entries"]) {
			_, _ = invoke("blocklist_remove", map[string]any{"ids": []any{numOr0(e["id"])}})
		}
	})

	if msg := callErr(t, "history_mark_failed", map[string]any{"history_id": 99999999}); !strings.Contains(msg, "no history entry") {
		t.Errorf("an unknown history id = %s", msg)
	}
	callErr(t, "history_mark_failed", map[string]any{"history_id": 0})
}

func TestHistoryList(t *testing.T) {
	out := call(t, "history_list", map[string]any{"limit": 5})

	entries := rows(t, out["entries"], "entries")
	if len(entries) == 0 || len(entries) > 5 || num(t, out["total"], "total") < len(entries) {
		t.Fatalf("history_list = %v", out)
	}
	for i := 1; i < len(entries); i++ {
		if str(entries[i]["date"]) > str(entries[i-1]["date"]) {
			t.Fatalf("not newest first at %d", i)
		}
	}
	// a scan of files already in a series folder records nothing, so what
	// Breaking Bad's downloads did is all there is to find, by event
	grabs := call(t, "history_list", map[string]any{"series": breakingBad.Title, "events": []any{"grabbed"}})
	for _, e := range rows(t, grabs["entries"], "entries") {
		if str(e["event"]) != "grabbed" || str(e["series"]) != breakingBad.Title || str(e["indexer"]) != fakeIndexerName {
			t.Errorf("a grab = %v", e)
		}
	}
	if n := num(t, grabs["total"], "total"); n < 3 {
		t.Errorf("Breaking Bad's grabs = %d, want the downloads tests' at least", n)
	}
	// and a window of days pages back only that far
	if recent := call(t, "history_list", map[string]any{"days": 1, "limit": 1}); num(t, recent["total"], "total") < 1 {
		t.Errorf("the last day's history = %v", recent)
	}
	for _, bad := range []map[string]any{{"events": []any{"exploded"}}, {"episode": "S01E01"}} {
		callErr(t, "history_list", bad)
	}
}
