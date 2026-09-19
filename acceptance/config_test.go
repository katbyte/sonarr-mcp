//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

func TestProfileList(t *testing.T) {
	out := call(t, "profile_list", nil)

	profiles := rows(t, out["profiles"], "profiles")
	hd := findRow(t, profiles, "name", profile)
	allowed := strs(t, hd["allowed_qualities"], "allowed_qualities")
	// best first, and nothing below 1080p
	if len(allowed) == 0 || !strings.Contains(allowed[0], "1080p") || slices.Contains(allowed, "SDTV") {
		t.Errorf("HD-1080p allows %v", allowed)
	}
	if str(hd["cutoff"]) != "HDTV-1080p" || num(t, hd["series"], "series") < 4 {
		t.Errorf("HD-1080p = %v", hd)
	}
	if any := findRow(t, profiles, "name", "Any"); num(t, any["series"], "series") != 0 || !slices.Contains(strs(t, any["allowed_qualities"], "allowed"), "SDTV") {
		t.Errorf("Any = %v", any)
	}
}

// A custom format no profile scores is listed with its specifications, and
// the profiles audit calls it dead weight; scored, it is neither.
func TestCustomFormatList(t *testing.T) {
	skipUnlessReady(t)

	schema, err := api.GetCustomFormatSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	specs := schema.Model
	i := slices.IndexFunc(specs, func(s sonarr.CustomFormatSpecificationSchema) bool {
		return s.Implementation == "ReleaseTitleSpecification"
	})
	if i < 0 {
		t.Fatal("no ReleaseTitleSpecification in the custom format schema")
	}
	spec := specs[i]
	spec.Name, spec.Negate, spec.Required = "x265", new(false), new(true)
	for j := range spec.Fields {
		if spec.Fields[j].Name == "value" {
			spec.Fields[j].Value = `\b(x265|HEVC)\b`
		}
	}
	made, err := api.PostCustomFormat(ctx, sonarr.CustomFormatResource{Name: "Test HEVC", Specifications: []sonarr.CustomFormatSpecificationSchema{spec}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = api.DeleteCustomFormatById(ctx, made.Model.Id) })

	out := call(t, "customformat_list", nil)
	cf := findRow(t, rows(t, out["custom_formats"], "custom_formats"), "name", "Test HEVC")
	if specsOut := strs(t, cf["specifications"], "specifications"); len(specsOut) != 1 || specsOut[0] != "ReleaseTitleSpecification: x265 (required)" {
		t.Errorf("specifications = %v", specsOut)
	}
	if len(object(t, cf["scores"], "scores")) != 0 {
		t.Errorf("scores = %v, want none", cf["scores"])
	}
	if f := findings(t, call(t, "audit_profiles", nil), "subject", "Test HEVC"); len(f) != 1 || str(f[0]["problem"]) != "custom format scored nowhere" {
		t.Errorf("audit_profiles on an unscored format = %v", f)
	}

	// scored in HD-1080p, it counts
	res, err := api.GetQualityProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := res.Model[slices.IndexFunc(res.Model, func(p sonarr.QualityProfileResource) bool { return p.Name == profile })]
	for j := range p.FormatItems {
		if p.FormatItems[j].Format == made.Model.Id {
			p.FormatItems[j].Score = -100
		}
	}
	if _, err := api.PutQualityProfileById(ctx, strconv.Itoa(p.Id), p); err != nil {
		t.Fatal(err)
	}
	scored := call(t, "customformat_list", nil)
	cf = findRow(t, rows(t, scored["custom_formats"], "custom_formats"), "name", "Test HEVC")
	if s := object(t, cf["scores"], "scores"); numOr0(s[profile]) != -100 {
		t.Errorf("scores after scoring = %v", s)
	}
	if hd := findRow(t, rows(t, call(t, "profile_list", nil)["profiles"], "profiles"), "name", profile); numOr0(object(t, hd["custom_format_scores"], "scores")["Test HEVC"]) != -100 {
		t.Errorf("profile_list scores = %v", hd["custom_format_scores"])
	}
	if f := findings(t, call(t, "audit_profiles", nil), "subject", "Test HEVC"); len(f) != 0 {
		t.Errorf("a scored format is still reported: %v", f)
	}
}

func TestTags(t *testing.T) {
	made := call(t, "tag_create", map[string]any{"label": "Kids Shows"})
	if str(made["label"]) != "kids shows" || num(t, made["id"], "id") == 0 {
		t.Fatalf("tag_create = %v", made)
	}
	if msg := callErr(t, "tag_create", map[string]any{"label": "KIDS SHOWS"}); !strings.Contains(msg, "already exists") {
		t.Errorf("a second tag with the label = %s", msg)
	}
	callErr(t, "tag_create", map[string]any{"label": "  "})

	call(t, "series_edit", map[string]any{"series": firefly.Title, "add_tags": []any{"kids shows"}})
	list := call(t, "tag_list", nil)
	tags := rows(t, list["tags"], "tags")
	kids := findRow(t, tags, "label", "kids shows")
	if !slices.Equal(strs(t, kids["series"], "series"), []string{firefly.Title}) || kids["in_use"] != true {
		t.Errorf("kids shows = %v", kids)
	}
	if four := findRow(t, tags, "label", unusedTag); four["in_use"] != false {
		t.Errorf("%s = %v", unusedTag, four)
	}
	if anime := findRow(t, tags, "label", "anime"); !slices.Contains(strs(t, anime["series"], "series"), cowboyBebop.Title) {
		t.Errorf("anime = %v", anime)
	}

	// Sonarr will not delete a tag something carries
	if msg := callErr(t, "tag_delete", map[string]any{"label": "kids shows"}); !strings.Contains(strings.ToLower(msg), "in use") {
		t.Errorf("deleting a tag in use = %s", msg)
	}
	call(t, "series_edit", map[string]any{"series": firefly.Title, "remove_tags": []any{"kids shows"}})
	if out := call(t, "tag_delete", map[string]any{"label": "Kids Shows"}); str(out["deleted"]) != "kids shows" {
		t.Errorf("tag_delete = %v", out)
	}
	if msg := callErr(t, "tag_delete", map[string]any{"label": "kids shows"}); !strings.Contains(msg, "no tag") {
		t.Errorf("deleting it again = %s", msg)
	}
}

func TestRootFolders(t *testing.T) {
	out := call(t, "rootfolder_list", nil)

	folders := rows(t, out["root_folders"], "root_folders")
	tv := findRow(t, folders, "path", "/tv")
	if tv["accessible"] != true || num(t, tv["series"], "series") < 5 || str(tv["free_space"]) == "" {
		t.Errorf("/tv = %v", tv)
	}
	if !slices.Contains(strs(t, tv["unmapped_folders"], "unmapped_folders"), theExpanse.Folder) {
		t.Errorf("/tv unmapped = %v", tv["unmapped_folders"])
	}

	// a second root folder, next to the downloads rather than inside them
	dir := filepath.Join(dataDir(), "downloads", "library2")
	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container writes it as another user
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // same
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	added := call(t, "rootfolder_add", map[string]any{"path": "/downloads/library2"})
	if r := object(t, added["root_folder"], "root_folder"); str(r["path"]) != "/downloads/library2" || numOr0(r["series"]) != 0 {
		t.Errorf("rootfolder_add = %v", added)
	}
	if msg := callErr(t, "rootfolder_add", map[string]any{"path": "/downloads/library2"}); !strings.Contains(msg, "HTTP 400") {
		t.Errorf("adding it twice = %s", msg)
	}
	callErr(t, "rootfolder_add", map[string]any{"path": ""})

	// with two, a series added without naming one has to say which
	if msg := callErr(t, "series_add", map[string]any{"series": "tvdb:280619"}); !strings.Contains(msg, "2 root folders") {
		t.Errorf("series_add with two root folders = %s", msg)
	}

	removed := call(t, "rootfolder_remove", map[string]any{"path": "/downloads/library2/"})
	if str(removed["removed"]) != "/downloads/library2" || numOr0(removed["series_still_in_it"]) != 0 {
		t.Errorf("rootfolder_remove = %v", removed)
	}
	if msg := callErr(t, "rootfolder_remove", map[string]any{"path": ""}); !strings.Contains(msg, "path is required") {
		t.Errorf("removing no path = %s", msg)
	}
	if msg := callErr(t, "rootfolder_remove", map[string]any{"path": "/nowhere"}); !strings.Contains(msg, "no root folder") {
		t.Errorf("removing an unknown path = %s", msg)
	}
}

func TestConfigGet(t *testing.T) {
	naming := call(t, "config_get", map[string]any{"section": "naming"})
	settings := object(t, naming["settings"], "settings")
	if !strings.Contains(str(settings["standardEpisodeFormat"]), "{Series Title}") || settings["renameEpisodes"] != true {
		t.Errorf("naming = %v", settings)
	}

	// the host section holds the API key, and it never comes back
	host := object(t, call(t, "config_get", map[string]any{"section": "host"})["settings"], "settings")
	for k := range host {
		if strings.Contains(strings.ToLower(k), "apikey") || strings.Contains(strings.ToLower(k), "password") {
			t.Errorf("the host settings include %s", k)
		}
	}
	if numOr0(host["port"]) != 8989 {
		t.Errorf("host = %v", host)
	}

	for _, section := range []string{"mediamanagement", "media_management", "ui", "indexer", "downloadclient", "importlist"} {
		out := call(t, "config_get", map[string]any{"section": section})
		if len(object(t, out["settings"], "settings")) == 0 {
			t.Errorf("%s has no settings", section)
		}
	}
	if dc := object(t, call(t, "config_get", map[string]any{"section": "downloadclient"})["settings"], "s"); dc["autoRedownloadFailed"] != false {
		t.Errorf("the suite turned automatic re-download off, and config_get says %v", dc["autoRedownloadFailed"])
	}
	if msg := callErr(t, "config_get", map[string]any{"section": "secrets"}); !strings.Contains(msg, "section must be") {
		t.Errorf("an unknown section = %s", msg)
	}
}
