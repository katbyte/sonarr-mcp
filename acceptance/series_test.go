//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/mcp/acctest"
)

func TestSeriesList(t *testing.T) {
	out := call(t, "series_list", nil)

	list := acctest.Rows(t, out["series"], "series")
	if acctest.Num(t, out["total"], "total") != len(list) || len(list) < len(importedSeed)+1 {
		t.Fatalf("series_list = %d of %v", len(list), out["total"])
	}
	// sorted by title, the way Sonarr sorts: Breaking Bad before Chernobyl
	var titles []string
	for _, s := range list {
		titles = append(titles, acctest.Str(s["title"]))
	}
	if slices.Index(titles, breakingBad.Title) > slices.Index(titles, chernobyl.Title) {
		t.Errorf("not sorted by title: %v", titles)
	}

	f := findRow(t, list, "title", firefly.Title)
	if acctest.Num(t, f["episodes_have"], "episodes_have") < 5 || acctest.Num(t, f["episodes_total"], "episodes_total") < 14 ||
		acctest.Str(f["quality_profile"]) != profile || acctest.Str(f["status"]) != "ended" || f["monitored"] != true {
		t.Errorf("Firefly = %v", f)
	}
	if b := findRow(t, list, "title", cowboyBebop.Title); !slices.Contains(acctest.Strs(t, b["tags"], "tags"), "anime") {
		t.Errorf("Cowboy Bebop tags = %v", b["tags"])
	}

	for _, c := range []struct {
		name string
		args map[string]any
		want []string
		not  []string
	}{
		{"query", map[string]any{"query": "fire"}, []string{firefly.Title}, []string{chernobyl.Title}},
		{"status", map[string]any{"status": "continuing"}, []string{severance.Title}, []string{firefly.Title}},
		{"tag", map[string]any{"tag": "ANIME"}, []string{cowboyBebop.Title}, []string{firefly.Title}},
		{"profile", map[string]any{"quality_profile": "hd-1080p"}, []string{firefly.Title}, nil},
		{"incomplete", map[string]any{"incomplete": true}, []string{breakingBad.Title}, []string{chernobyl.Title}},
		{"monitored", map[string]any{"monitored": false}, nil, []string{firefly.Title}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := call(t, "series_list", c.args)
			var titles []string
			for _, s := range acctest.Rows(t, got["series"], "series") {
				titles = append(titles, acctest.Str(s["title"]))
			}
			for _, w := range c.want {
				if !slices.Contains(titles, w) {
					t.Errorf("%v lacks %s: %v", c.args, w, titles)
				}
			}
			for _, n := range c.not {
				if slices.Contains(titles, n) {
					t.Errorf("%v includes %s: %v", c.args, n, titles)
				}
			}
		})
	}

	// a page, the total that says how many there are, and where the next
	// page starts; the last page names none
	page := call(t, "series_list", map[string]any{"limit": 2, "offset": 1})
	if len(acctest.Rows(t, page["series"], "series")) != 2 || acctest.Num(t, page["total"], "total") != len(list) || acctest.Num(t, page["offset"], "offset") != 1 || acctest.Num(t, page["next_offset"], "next_offset") != 3 {
		t.Errorf("page = %v", page)
	}
	last := call(t, "series_list", map[string]any{"limit": 2, "offset": len(list) - 1})
	if _, more := last["next_offset"]; more || len(acctest.Rows(t, last["series"], "series")) != 1 {
		t.Errorf("the last page = %v", last)
	}
}

func TestSeriesGet(t *testing.T) {
	out := call(t, "series_get", map[string]any{"series": firefly.Title})

	if acctest.Num(t, acctest.Object(t, out["ids"], "ids")["tvdb"], "tvdb") != firefly.TvdbID || acctest.NumOr0(out["year"]) != firefly.Year {
		t.Errorf("Firefly ids = %v year %v", out["ids"], out["year"])
	}
	if acctest.Str(out["path"]) != "/tv/"+firefly.Folder || acctest.Str(out["root_folder"]) != "/tv" || acctest.Str(out["network"]) == "" || acctest.NumOr0(out["runtime_minutes"]) == 0 {
		t.Errorf("Firefly = %v", out)
	}
	if !slices.Contains(acctest.Strs(t, out["genres"], "genres"), "Drama") || acctest.Str(out["original_language"]) != "English" {
		t.Errorf("genres %v language %v", out["genres"], out["original_language"])
	}
	seasons := acctest.Rows(t, out["seasons"], "seasons")
	var one map[string]any
	for _, s := range seasons {
		if acctest.Num(t, s["season"], "season") == 1 {
			one = s
		}
	}
	if one == nil || acctest.Num(t, one["episodes_total"], "episodes_total") != 14 || one["monitored"] != true {
		t.Errorf("season 1 = %v", one)
	}

	// the same series by every name it goes by
	for _, ref := range []string{"firefly", "Firefly (2002)", "firefly 2002", "tvdb:78874", "TVDB:78874", strconv.Itoa(acctest.Num(t, out["id"], "id")), "firef"} {
		got := call(t, "series_get", map[string]any{"series": ref})
		if acctest.Str(got["title"]) != firefly.Title {
			t.Errorf("%q resolved to %v", ref, got["title"])
		}
	}
	// and what is not there, or not one thing, says so
	if msg := callErr(t, "series_get", map[string]any{"series": "The Wire"}); !strings.Contains(msg, "no series in Sonarr matches") {
		t.Errorf("an unknown title = %s", msg)
	}
	if msg := callErr(t, "series_get", map[string]any{"series": "firefly 1999"}); !strings.Contains(msg, "no series") {
		t.Errorf("the right title with the wrong year = %s", msg)
	}
	if msg := callErr(t, "series_get", map[string]any{"series": "99999"}); !strings.Contains(msg, "no series with id 99999") {
		t.Errorf("an unknown id = %s", msg)
	}
	if msg := callErr(t, "series_get", map[string]any{"series": ""}); !strings.Contains(msg, "name a series") {
		t.Errorf("no series = %s", msg)
	}
	// "s" starts a word of Severance and of several others
	if msg := callErr(t, "series_get", map[string]any{"series": "b"}); !strings.Contains(msg, "matches") || !strings.Contains(msg, breakingBad.Title) {
		t.Errorf("an ambiguous prefix = %s", msg)
	}
}

func TestSeriesLookup(t *testing.T) {
	out := call(t, "series_lookup", map[string]any{"term": "tvdb:" + strconv.Itoa(firefly.TvdbID)})

	c := acctest.Rows(t, out["candidates"], "candidates")
	if len(c) != 1 || acctest.Str(c[0]["title"]) != firefly.Title || c[0]["in_sonarr"] != true || acctest.NumOr0(c[0]["id"]) == 0 {
		t.Errorf("lookup tvdb:78874 = %v", c)
	}
	named := call(t, "series_lookup", map[string]any{"term": theExpanse.Title, "limit": 3})
	c = acctest.Rows(t, named["candidates"], "candidates")
	if len(c) == 0 || len(c) > 3 || acctest.Str(c[0]["title"]) != theExpanse.Title || c[0]["in_sonarr"] != false {
		t.Errorf("lookup The Expanse = %v", c)
	}
	callErr(t, "series_lookup", map[string]any{"term": " "})
}

// Adding a show, editing everything series_edit edits, and deleting it,
// files and all.
func TestSeriesAddEditDelete(t *testing.T) {
	bob := bandOfBrothers.TvdbID
	added := call(t, "series_add", map[string]any{
		"series": "tvdb:" + strconv.Itoa(bob), "quality_profile": "HD-720p", "monitor": "firstSeason",
		"tags": []any{"war"}, "season_folder": false,
	})
	a := acctest.Object(t, added["added"], "added")
	if acctest.Str(a["title"]) != "Band of Brothers" || acctest.Str(a["quality_profile"]) != "HD-720p" || acctest.Str(a["path"]) != "/tv/Band of Brothers" ||
		!slices.Contains(acctest.Strs(t, a["tags"], "tags"), "war") || added["searched"] != false {
		t.Fatalf("added = %v", added)
	}
	t.Cleanup(func() {
		_, _ = invoke("series_delete", map[string]any{"series": "tvdb:" + strconv.Itoa(bob), "delete_files": true, "confirm": true})
		_, _ = invoke("tag_delete", map[string]any{"label": "war", "confirm": true})
	})

	// adding it again is refused, by id or by title
	if msg := callErr(t, "series_add", map[string]any{"series": "tvdb:" + strconv.Itoa(bob)}); !strings.Contains(msg, "already in Sonarr") {
		t.Errorf("a second add = %s", msg)
	}
	// a title two shows share is refused with the candidates, even when one
	// of them has it without TheTVDB's "(US)" suffix
	if msg := callErr(t, "series_add", map[string]any{"series": "The Office"}); !strings.Contains(msg, "The Office (2001) tvdb:78107") || strings.Contains(msg, "(2012) (2012)") {
		t.Errorf("an ambiguous add = %s", msg)
	}
	if msg := callErr(t, "series_add", map[string]any{"series": "tvdb:" + strconv.Itoa(theExpanse.TvdbID), "monitor": "sometimes"}); !strings.Contains(msg, "monitor must be one of") {
		t.Errorf("an unknown monitor choice = %s", msg)
	}

	edited := call(t, "series_edit", map[string]any{
		"series": "Band of Brothers", "monitored": false, "quality_profile": "HD-1080p", "series_type": "standard",
		"season_folder": true, "monitor_new_items": "none", "add_tags": []any{"archive"}, "remove_tags": []any{"war"},
	})
	t.Cleanup(func() { _, _ = invoke("tag_delete", map[string]any{"label": "archive", "confirm": true}) })
	changed := acctest.Strs(t, edited["changed"], "changed")
	for _, want := range []string{"monitored: true -> false", "quality_profile: HD-720p -> HD-1080p", "season_folder: false -> true", "monitor_new_items: all -> none", "tags: [war] -> [archive]"} {
		if !slices.Contains(changed, want) {
			t.Errorf("changed %v lacks %q", changed, want)
		}
	}
	s := acctest.Object(t, edited["series"], "series")
	if s["monitored"] != false || acctest.Str(s["quality_profile"]) != "HD-1080p" || !slices.Equal(acctest.Strs(t, s["tags"], "tags"), []string{"archive"}) {
		t.Errorf("after the edit = %v", s)
	}
	// an edit that changes nothing says so
	if same := call(t, "series_edit", map[string]any{"series": "Band of Brothers", "monitored": false}); len(acctest.RowsOf(same["changed"])) != 0 {
		t.Errorf("a no-op edit changed %v", same["changed"])
	}
	callErr(t, "series_edit", map[string]any{"series": "Band of Brothers", "series_type": "weekly"})
	callErr(t, "series_edit", map[string]any{"series": "Band of Brothers", "quality_profile": "4K Only"})

	// a path change without moving the files only moves the record; the
	// folder did not exist yet, so nothing moves either way
	moved := call(t, "series_edit", map[string]any{"series": "Band of Brothers", "path": "/tv/Band of Brothers (2001)"})
	if acctest.Str(acctest.Object(t, moved["series"], "series")["path"]) != "/tv/Band of Brothers (2001)" {
		t.Errorf("path edit = %v", moved)
	}

	// without confirm the series is named, with what would go from disk, and
	// is still there
	would := call(t, "series_delete", map[string]any{"series": "Band of Brothers", "delete_files": true, "add_import_exclusion": true})
	if acctest.Str(would["would_delete"]) != "Band of Brothers (2001)" || acctest.Str(would["deleted"]) != "" || would["files_deleted"] != false || !strings.Contains(acctest.Str(would["files"]), "/tv/Band of Brothers (2001)") {
		t.Errorf("without confirm = %v", would)
	}
	call(t, "series_get", map[string]any{"series": "Band of Brothers"})

	deleted := call(t, "series_delete", map[string]any{"series": "Band of Brothers", "delete_files": true, "add_import_exclusion": true, "confirm": true})
	if acctest.Str(deleted["deleted"]) != "Band of Brothers (2001)" || deleted["files_deleted"] != true {
		t.Errorf("deleted = %v", deleted)
	}
	if msg := callErr(t, "series_get", map[string]any{"series": "Band of Brothers"}); !strings.Contains(msg, "no series") {
		t.Errorf("the deleted series still resolves: %s", msg)
	}
}

func TestSeriesRefreshAndRescan(t *testing.T) {
	out := call(t, "series_refresh", map[string]any{"series": chernobyl.Title})

	cmd := acctest.Object(t, out["command"], "command")
	if acctest.Str(cmd["status"]) != "completed" || acctest.Str(cmd["command"]) != "Refresh Series" {
		t.Errorf("refresh = %v", cmd)
	}
	if acctest.Num(t, acctest.Object(t, out["series"], "series")["episodes_have"], "episodes_have") != chernobyl.Files {
		t.Errorf("after the refresh = %v", out["series"])
	}

	// queued without waiting, it answers at once with the command queued or
	// started
	quick := call(t, "series_rescan", map[string]any{"series": chernobyl.Title, "wait_seconds": -1})
	if s := acctest.Str(acctest.Object(t, quick["command"], "command")["status"]); s != "queued" && s != "started" && s != "completed" {
		t.Errorf("rescan without waiting = %v", quick["command"])
	}
}

// Renaming Chernobyl's scene-named files to the format, after a dry run
// that shows the same plan without touching anything.
func TestSeriesRename(t *testing.T) {
	plan := call(t, "series_rename", map[string]any{"series": chernobyl.Title, "dry_run": true})
	renames := acctest.Rows(t, plan["renamed"], "renamed")
	if len(renames) != chernobyl.Files || plan["command"] != nil {
		t.Fatalf("dry run = %v", plan)
	}
	e05 := findRow(t, renames, "episodes", "S01E05")
	if !strings.Contains(acctest.Str(e05["from"]), "GERMAN") || !strings.HasSuffix(acctest.Str(e05["to"]), "Chernobyl - S01E05 - Vichnaya Pamyat WEBDL-1080p.mkv") {
		t.Errorf("E05 rename = %v", e05)
	}
	if _, err := os.Stat(hostPath(acctest.Str(e05["from"]))); err != nil {
		t.Errorf("the dry run moved %s: %v", e05["from"], err)
	}

	// one file only, by id
	one := call(t, "series_rename", map[string]any{"series": chernobyl.Title, "file_ids": []any{acctest.Num(t, e05["file_id"], "file_id")}})
	if len(acctest.Rows(t, one["renamed"], "renamed")) != 1 || acctest.Str(acctest.Object(t, one["command"], "command")["status"]) != "completed" {
		t.Fatalf("renaming E05 = %v", one)
	}
	if _, err := os.Stat(hostPath(acctest.Str(e05["to"]))); err != nil {
		t.Errorf("E05 is not at its new name: %v", err)
	}
	if msg := callErr(t, "series_rename", map[string]any{"series": chernobyl.Title, "file_ids": []any{acctest.Num(t, e05["file_id"], "file_id")}}); !strings.Contains(msg, "already named to the format") {
		t.Errorf("renaming it again = %s", msg)
	}

	// and the audit no longer counts it
	after := call(t, "audit_naming", map[string]any{"series": chernobyl.Title})
	if f := findings(t, after, "series", chernobyl.Title); len(f) != 1 || !strings.Contains(acctest.Str(f[0]["detail"]), "4 of 5 files") {
		t.Errorf("after renaming E05 = %v", after["findings"])
	}
	t.Cleanup(func() {
		// put the scene name back, so the naming audit's fixture holds
		_ = os.Rename(hostPath(acctest.Str(e05["to"])), hostPath(acctest.Str(e05["from"])))
		_, _ = invoke("series_rescan", map[string]any{"series": chernobyl.Title})
	})
	if _, err := os.Stat(filepath.Dir(hostPath(acctest.Str(e05["to"])))); err != nil {
		t.Error(err)
	}
}
