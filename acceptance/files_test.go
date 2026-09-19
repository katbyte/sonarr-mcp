//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFileList(t *testing.T) {
	out := call(t, "file_list", map[string]any{"series": firefly.Title})

	files := rows(t, out["files"], "files")
	if str(out["path"]) != "/tv/"+firefly.Folder || len(files) != firefly.Files {
		t.Fatalf("Firefly files = %d: %v", len(files), out)
	}
	// in episode order, each with what its streams say
	for i, f := range files {
		if want := episodeName(1, i+1); str(f["episodes"]) != want {
			t.Errorf("file %d is %v, want %s", i, f["episodes"], want)
		}
	}
	e04 := findRow(t, files, "episodes", "S01E04")
	media := object(t, e04["media"], "media")
	if str(e04["quality"]) != "SDTV" || e04["below_cutoff"] != true || str(media["resolution"]) != "640x360" ||
		str(media["video_codec"]) != "x264" || str(media["runtime"]) == "" {
		t.Errorf("S01E04 = %v", e04)
	}
	if e01 := findRow(t, files, "episodes", "S01E01"); !slices.Contains(strs(t, e01["languages"], "languages"), "English") {
		t.Errorf("S01E01 languages = %v", e01["languages"])
	}

	// the specials hold nothing
	if specials := call(t, "file_list", map[string]any{"series": firefly.Title, "season": 0}); len(rowsOf(specials["files"])) != 0 {
		t.Errorf("Firefly specials = %v", specials["files"])
	}
}

func TestFileEdit(t *testing.T) {
	e02 := fireflyFile(t, 2)
	out := call(t, "file_edit", map[string]any{"file_ids": []any{e02}, "languages": []any{"japanese"}, "release_group": "FAKE"})

	files := rows(t, out["files"], "files")
	if len(files) != 1 || str(files[0]["episodes"]) != "S01E02" || !slices.Equal(strs(t, files[0]["languages"], "languages"), []string{"Japanese"}) ||
		str(files[0]["release_group"]) != "FAKE" || str(files[0]["quality"]) != "WEBDL-1080p" {
		t.Fatalf("file_edit = %v", out)
	}
	t.Cleanup(func() { call(t, "file_edit", map[string]any{"file_ids": []any{e02}, "languages": []any{"English"}}) })

	// the language audit sees the file as recorded now
	lang := call(t, "audit_language", map[string]any{"series": firefly.Title})
	if f := findings(t, lang, "subject", "S01E02 "); len(f) != 1 || str(f[0]["problem"]) != "recorded in another language" {
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
	e03 := findRow(t, rows(t, list["files"], "files"), "episodes", "S01E03")

	out := call(t, "file_delete", map[string]any{"file_ids": []any{num(t, e03["id"], "id")}})
	if deleted := strs(t, out["deleted"], "deleted"); len(deleted) != 1 || !strings.HasSuffix(deleted[0], "In Perpetuity WEBDL-1080p.mkv") {
		t.Errorf("deleted = %v", out)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("the file is still on disk: %v", err)
	}
	eps := call(t, "episode_list", map[string]any{"series": severance.Title, "season": 1})
	if e := findRow(t, rows(t, eps["episodes"], "episodes"), "episode", "S01E03"); e["has_file"] != false {
		t.Errorf("S01E03 after its file was deleted = %v", e)
	}

	if msg := callErr(t, "file_delete", map[string]any{"file_ids": []any{999999}}); !strings.Contains(msg, "no episode file 999999") {
		t.Errorf("an unknown file = %s", msg)
	}
	callErr(t, "file_delete", map[string]any{"file_ids": []any{}})
}
