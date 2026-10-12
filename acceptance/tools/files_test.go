//go:build acceptance

package tools_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/sonarr-mcp/lib/testenv"
)

func TestFileList(t *testing.T) {
	out := call(t, "file_list", map[string]any{"series": firefly.Title})

	files := acctest.Rows(t, out["files"], "files")
	if acctest.Str(out["path"]) != "/tv/"+firefly.Folder || len(files) != firefly.Files {
		t.Fatalf("Firefly files = %d: %v", len(files), out)
	}
	// in episode order, each with what its streams say
	for i, f := range files {
		if want := episodeName(1, i+1); acctest.Str(f["episodes"]) != want {
			t.Errorf("file %d is %v, want %s", i, f["episodes"], want)
		}
	}
	e04 := findRow(t, files, "episodes", "S01E04")
	media := acctest.Object(t, e04["media"], "media")
	if acctest.Str(e04["quality"]) != "SDTV" || !acctest.BoolOf(e04["below_cutoff"]) || acctest.Str(media["resolution"]) != "640x360" ||
		acctest.Str(media["video_codec"]) != "x264" || acctest.Str(media["runtime"]) == "" {
		t.Errorf("S01E04 = %v", e04)
	}
	if e01 := findRow(t, files, "episodes", "S01E01"); !slices.Contains(acctest.Strs(t, e01["languages"], "languages"), "English") {
		t.Errorf("S01E01 languages = %v", e01["languages"])
	}

	// the specials hold nothing
	if specials := call(t, "file_list", map[string]any{"series": firefly.Title, "season": 0}); len(acctest.RowsOf(specials["files"])) != 0 {
		t.Errorf("Firefly specials = %v", specials["files"])
	}
}

func TestFileEdit(t *testing.T) {
	e02 := fireflyFile(t, 2)
	out := call(t, "file_edit", map[string]any{"file_ids": []any{e02}, "languages": []any{"japanese"}, "release_group": "FAKE"})

	files := acctest.Rows(t, out["files"], "files")
	if len(files) != 1 || acctest.Str(files[0]["episodes"]) != "S01E02" || !slices.Equal(acctest.Strs(t, files[0]["languages"], "languages"), []string{"Japanese"}) ||
		acctest.Str(files[0]["release_group"]) != "FAKE" || acctest.Str(files[0]["quality"]) != "WEBDL-1080p" {
		t.Fatalf("file_edit = %v", out)
	}
	t.Cleanup(func() { call(t, "file_edit", map[string]any{"file_ids": []any{e02}, "languages": []any{"English"}}) })

	// the language audit sees the file as recorded now
	lang := call(t, "audit_language", map[string]any{"series": firefly.Title})
	if f := findings(t, lang, "subject", "S01E02 "); len(f) != 1 || acctest.Str(f[0]["problem"]) != "recorded in another language" {
		t.Errorf("after recording S01E02 as Japanese = %v", lang["findings"])
	}

	for _, bad := range []map[string]any{
		{"file_ids": []any{e02}},
		{"file_ids": []any{}, "quality": "SDTV"},
		{"file_ids": []any{e02}, "quality": "VHS-240p"},
		{"file_ids": []any{e02}, "languages": []any{"Klingon"}},
	} {
		callErr(t, "file_edit", bad)
	}
}

// Deleting a file takes it off the disk, and its episode is missing again.
func TestFileDelete(t *testing.T) {
	skipUnlessReady(t)

	// a file of its own, so the fixtures are left as they are
	season := filepath.Join(tvDir(), severance.Folder, "Season 1")
	p := filepath.Join(season, "Severance - S01E03 - In Perpetuity WEBDL-1080p.mkv")
	if err := fakeVideo(p, 57, "1920x1080"); err != nil {
		t.Fatal(err)
	}
	call(t, "series_rescan", map[string]any{"series": severance.Title})
	list := call(t, "file_list", map[string]any{"series": severance.Title, "season": 1})
	e03 := findRow(t, acctest.Rows(t, list["files"], "files"), "episodes", "S01E03")

	// the episode is monitored, as one being deleted to make room usually
	// still is, and is put back as it was afterwards
	eps := call(t, "episode_list", map[string]any{"series": severance.Title, "season": 1})
	was := findRow(t, acctest.Rows(t, eps["episodes"], "episodes"), "episode", "S01E03")["monitored"]
	call(t, "episode_monitor", map[string]any{"series": severance.Title, "episodes": []any{"S01E03"}, "monitored": true})
	t.Cleanup(func() {
		call(t, "episode_monitor", map[string]any{"series": severance.Title, "episodes": []any{"S01E03"}, "monitored": was})
	})

	// without confirm the file is named and left where it is, with the
	// episode it holds and the warning that Sonarr would get it again
	would := call(t, "file_delete", map[string]any{"file_ids": []any{acctest.Num(t, e03["id"], "id")}})
	if gone := acctest.Strs(t, would["would_delete"], "would_delete"); len(gone) != 1 || !strings.HasSuffix(gone[0], "In Perpetuity WEBDL-1080p.mkv") || len(acctest.Strs(t, would["deleted"], "deleted")) != 0 {
		t.Errorf("without confirm = %v", would)
	}
	if row := acctest.Rows(t, would["files"], "files"); len(row) != 1 || acctest.Str(row[0]["episodes"]) != "S01E03" || !acctest.BoolOf(row[0]["monitored"]) || acctest.Str(row[0]["size"]) == "" {
		t.Errorf("the file as the preview names it = %v", would["files"])
	}
	if w := acctest.Str(would["warning"]); !strings.Contains(w, "S01E03") || !strings.Contains(w, "episode_monitor") {
		t.Errorf("the warning for a monitored episode = %q", w)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the file went without confirm: %v", err)
	}

	out := call(t, "file_delete", map[string]any{"file_ids": []any{acctest.Num(t, e03["id"], "id")}, "confirm": true})
	if deleted := acctest.Strs(t, out["deleted"], "deleted"); len(deleted) != 1 || !strings.HasSuffix(deleted[0], "In Perpetuity WEBDL-1080p.mkv") {
		t.Errorf("deleted = %v", out)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("the file is still on disk: %v", err)
	}
	// the episode is missing and Sonarr still wants it, which is what the
	// warning said: it is left monitored, to be downloaded again
	eps = call(t, "episode_list", map[string]any{"series": severance.Title, "season": 1})
	if e := findRow(t, acctest.Rows(t, eps["episodes"], "episodes"), "episode", "S01E03"); !acctest.IsBool(e["has_file"], false) || !acctest.BoolOf(e["monitored"]) {
		t.Errorf("S01E03 after its file was deleted = %v", e)
	}

	// with Sonarr set to unmonitor what is deleted, the same delete stops the
	// episode being monitored instead, and the warning says that
	media, err := api.GetConfigMediaManagement(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kept := testenv.Answered(media.Model)
	on := kept
	on.AutoUnmonitorPreviouslyDownloadedEpisodes = new(true)
	if _, err := api.PutConfigMediaManagementById(ctx, strconv.Itoa(on.Id), on); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = api.PutConfigMediaManagementById(ctx, strconv.Itoa(kept.Id), kept) })
	if err := fakeVideo(p, 57, "1920x1080"); err != nil {
		t.Fatal(err)
	}
	call(t, "series_rescan", map[string]any{"series": severance.Title})
	list = call(t, "file_list", map[string]any{"series": severance.Title, "season": 1})
	e03 = findRow(t, acctest.Rows(t, list["files"], "files"), "episodes", "S01E03")
	out = call(t, "file_delete", map[string]any{"file_ids": []any{acctest.Num(t, e03["id"], "id")}, "confirm": true})
	if w := acctest.Str(out["warning"]); !strings.Contains(w, "stops monitoring S01E03") {
		t.Errorf("the warning with Sonarr unmonitoring what is deleted = %q", w)
	}
	eps = call(t, "episode_list", map[string]any{"series": severance.Title, "season": 1})
	if e := findRow(t, acctest.Rows(t, eps["episodes"], "episodes"), "episode", "S01E03"); !acctest.IsBool(e["has_file"], false) || !acctest.IsBool(e["monitored"], false) {
		t.Errorf("S01E03 after its file was deleted with Sonarr unmonitoring what is deleted = %v", e)
	}

	if msg := callErr(t, "file_delete", map[string]any{"file_ids": []any{999999}, "confirm": true}); !strings.Contains(msg, "no episode file 999999") {
		t.Errorf("an unknown file = %s", msg)
	}
	callErr(t, "file_delete", map[string]any{"file_ids": []any{}})
}
