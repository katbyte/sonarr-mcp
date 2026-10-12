//go:build acceptance

package tools_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/sonarr-mcp/lib/testenv"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// The one journey every server in the family has: a setup built from
// nothing with the tools alone, in the order a person would, audited, its
// own mistake found and fixed, then taken down again with the tools, and
// Sonarr left exactly as it was found.
//
// Here that is a tag, a custom format and a quality profile scoring it, a
// delay and a release profile for the tag, a tracker and a download client
// carrying it, a webhook for the tag, an import list with an exclusion, a
// series added on the profile, an episode searched for, grabbed and
// imported, and a backup. The setup is made the naive way and the audits
// say what is wrong with it: the tag scopes settings before any series has
// it, the custom format is scored by no profile, and the file that came in
// is below the profile's cutoff. Each is fixed with the tool the finding
// names, and the audits are clean before it all comes down.
func TestJourneySetupAuditFixTeardown(t *testing.T) {
	const tag, format, profile, tracker, client, hook, list = "journey", "Journey HEVC", "Journey HD", "Journey Tracker", "Journey Blackhole", "Journey Hook", "Journey List"
	const episode = "S01E01"
	const lowTitle, highTitle = "Band.of.Brothers.S01E01.Currahee.720p.WEB-DL.DD5.1.H.264-JOURNEY", "Band.of.Brothers.S01E01.Currahee.1080p.WEB-DL.DD5.1.H.264-JOURNEY"
	bob := "tvdb:" + itoa(bandOfBrothers.TvdbID)

	before := setupState(t)

	// the tracker and the folders the blackhole reads and watches, beside
	// the suite's own
	low := release(lowTitle, bandOfBrothers.TvdbID, 1, 1, 1.1, newznab.CategoryTVHD)
	high := release(highTitle, bandOfBrothers.TvdbID, 1, 1, 2.4, newznab.CategoryTVHD)
	low.Seeders, low.Peers, high.Seeders, high.Peers = 20, 25, 20, 25
	torrents, err := fakes.AddTorrentIndexer("journey", low)
	if err != nil {
		t.Fatal(err)
	}
	inbox, watch := testenv.DataDir("downloads", "journey-torrents"), testenv.DataDir("downloads", "journey-watch")
	for _, dir := range []string{inbox, watch} {
		if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container writes to it as another user
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil { //nolint:gosec // the same
			t.Fatal(err)
		}
	}
	var heard atomic.Int32
	const hookHost = "journey.sonarr-mcp.test"
	stopHook := proxy.Serve(hookHost, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { heard.Add(1) }))

	// whatever the journey leaves behind when it fails part way is taken
	// down the same way, so the tests after it find Sonarr as it was
	t.Cleanup(func() {
		stopHook()
		_ = os.RemoveAll(inbox)
		_ = os.RemoveAll(watch)
		for _, step := range teardown(bob, list, hook, client, tracker, profile, format, "") {
			_, _ = invoke(step.tool, step.args)
		}
	})

	// 1. the pieces, each with the tag, in the order a person adds them
	call(t, "tag_create", map[string]any{"label": tag})
	t.Cleanup(func() { _, _ = invoke("tag_delete", map[string]any{"label": tag, "confirm": true}) })
	call(t, "customformat_add", map[string]any{"name": format, "set": map[string]any{"specifications": []any{
		map[string]any{"implementation": "ReleaseTitleSpecification", "name": "hevc", "required": true, "settings": map[string]any{"value": `\b(hevc|x265)\b`}},
	}}})
	made := call(t, "profile_create", map[string]any{"name": profile, "copy_of": "HD-1080p", "upgrade_allowed": true, "allowed_qualities": []any{"HDTV-720p", "WEBDL-720p", "WEBDL-1080p"}, "cutoff": "WEBDL-1080p"})
	if p := acctest.Object(t, made["profile"], "profile"); !acctest.BoolOf(p["upgrade_allowed"]) || acctest.Str(p["cutoff"]) != "WEB 1080p" {
		t.Fatalf("profile_create = %v", made)
	}
	delay := record(t, call(t, "delayprofile_add", map[string]any{"set": map[string]any{"usenetDelay": 0, "torrentDelay": 0, "preferredProtocol": "torrent", "tags": []any{tag}}}))
	delayID := itoa(acctest.Num(t, delay["id"], "id"))
	t.Cleanup(func() { _, _ = invoke("delayprofile_delete", map[string]any{"which": delayID, "confirm": true}) })
	call(t, "releaseprofile_add", map[string]any{"name": "Journey Rules", "set": map[string]any{"ignored": []any{"CAM"}, "tags": []any{tag}}})
	t.Cleanup(func() {
		_, _ = invoke("releaseprofile_delete", map[string]any{"which": "Journey Rules", "confirm": true})
	})
	call(t, "indexer_add", map[string]any{
		"kind": "Torznab", "name": tracker, "set": map[string]any{"enableRss": false, "tags": []any{tag}},
		"settings": map[string]any{"baseUrl": torrents.URL(), "apiPath": "/api", "apiKey": testenv.FakeKey, "categories": []any{5030, 5040}},
	})
	call(t, "downloadclient_add", map[string]any{"kind": "TorrentBlackhole", "name": client, "set": map[string]any{"tags": []any{tag}}, "settings": map[string]any{"torrentFolder": "/downloads/journey-torrents", "watchFolder": "/downloads/journey-watch"}})
	call(t, "notification_add", map[string]any{"kind": "Webhook", "name": hook, "set": map[string]any{"onGrab": true, "onDownload": true, "tags": []any{tag}}, "settings": map[string]any{"url": "http://" + hookHost + "/in", "method": 1}})
	if heard.Load() == 0 {
		t.Error("the webhook was saved without Sonarr sending a test through it")
	}
	call(t, "importlist_add", map[string]any{
		"kind": "SonarrImport", "name": list,
		"set":      map[string]any{"enableAutomaticAdd": false, "searchForMissingEpisodes": false, "rootFolderPath": "/tv", "qualityProfileId": profile, "shouldMonitor": "none", "monitorNewItems": "none", "seriesType": "standard", "seasonFolder": true, "tags": []any{tag}},
		"settings": map[string]any{"baseUrl": "http://localhost:8989", "apiKey": testenv.Token()},
	})
	call(t, "exclusion_add", map[string]any{"series": "tvdb:" + itoa(chernobyl.TvdbID)})
	t.Cleanup(func() {
		_, _ = invoke("exclusion_remove", map[string]any{"exclusion": "tvdb:" + itoa(chernobyl.TvdbID)})
	})

	// 2. the audit says what the setup still lacks: the format is scored by
	// no profile, which is fixed with the tool it names; and the tag scopes
	// settings while no series carries it, which the next step is
	settings := audit(t, "audit_profiles", nil)
	unscored := only(t, settings, "subject", format)
	if acctest.Str(unscored["problem"]) != "custom format scored nowhere" || !strings.Contains(acctest.Str(unscored["fix"]), "profile_edit") {
		t.Errorf("the format before a profile scores it = %v", unscored)
	}
	scored := call(t, "profile_edit", map[string]any{"profile": profile, "custom_format_scores": map[string]any{format: 50}})
	if !changedWith(scored, "custom_format_scores:") {
		t.Fatalf("profile_edit = %v", scored)
	}
	none(t, audit(t, "audit_profiles", nil), "subject", format)
	scoped := only(t, settings, "subject", tag)
	if acctest.Str(scoped["problem"]) != "tag scopes settings but no series" || !strings.Contains(acctest.Str(scoped["fix"]), "series_edit") {
		t.Errorf("the tag before a series has it = %v", scoped)
	}

	// 3. a series on the profile, with the tag, its first episode monitored
	add := call(t, "series_add", map[string]any{"series": bob, "root_folder": "/tv", "quality_profile": profile, "monitor": "pilot", "tags": []any{tag}})
	added := acctest.Object(t, add["added"], "added")
	if acctest.Str(added["title"]) != bandOfBrothers.Title || acctest.Str(added["quality_profile"]) != profile || !slices.Contains(acctest.Strs(t, added["tags"], "tags"), tag) || !acctest.BoolOf(add["ready"]) || acctest.NumOr0(added["episodes_total"]) == 0 {
		t.Fatalf("series_add = %v", add)
	}
	minutes := acctest.NumOr0(acctest.Object(t, call(t, "series_get", map[string]any{"series": bandOfBrothers.Title}), "series_get")["runtime_minutes"])
	if minutes == 0 {
		minutes = 60
	}
	none(t, audit(t, "audit_profiles", nil), "subject", tag)
	// (series_add waited for Sonarr to read the show: it sets what the add
	// asked to be monitored only then, over anything changed before)
	// only the pilot is wanted for now: the season monitored, so new
	// episodes would be, and every other episode in it not
	call(t, "season_monitor", map[string]any{"series": bandOfBrothers.Title, "seasons": []any{1}, "monitored": true})
	var others []any
	for _, e := range acctest.Rows(t, call(t, "episode_list", map[string]any{"series": bandOfBrothers.Title, "season": 1})["episodes"], "episodes") {
		if label := acctest.Str(e["episode"]); label != episode {
			others = append(others, label)
		}
	}
	call(t, "episode_monitor", map[string]any{"series": bandOfBrothers.Title, "episodes": others, "monitored": false})
	if seasons := acctest.Rows(t, call(t, "series_get", map[string]any{"series": bandOfBrothers.Title})["seasons"], "seasons"); !slices.ContainsFunc(seasons, func(s map[string]any) bool {
		return acctest.NumOr0(s["season"]) == 1 && acctest.BoolOf(s["monitored"]) && acctest.NumOr0(s["episodes_wanted"]) == 1
	}) {
		t.Fatalf("the seasons after monitoring the pilot alone = %v", seasons)
	}

	// 4. the episode searched for, grabbed through the tracker to the
	// blackhole, and what a torrent client leaves where Sonarr watches
	// (a blackhole has no queue of its own: the grab shows as the .torrent
	// handed to it, and as the webhook hearing of it)
	grabbed := heard.Load()
	handed := func(want int) {
		t.Helper()

		call(t, "episode_search", map[string]any{"series": bandOfBrothers.Title, "episodes": []any{episode}})
		if dropped, _ := filepath.Glob(filepath.Join(inbox, "*.torrent")); len(dropped) != want {
			found := call(t, "release_search", map[string]any{"series": bandOfBrothers.Title, "episode": episode})
			t.Fatalf("%d .torrent files were handed to the blackhole, want %d; the releases: %v", len(dropped), want, found["releases"])
		}
		if heard.Load() == grabbed {
			t.Error("the webhook did not hear of the grab")
		}
		grabbed = heard.Load()
	}
	handed(1)
	if err := fakeVideo(filepath.Join(watch, lowTitle, lowTitle+".mkv"), minutes, "1280x720"); err != nil {
		t.Fatal(err)
	}
	fileOf := func(quality string) map[string]any {
		var got map[string]any
		eventually(t, "file_list", map[string]any{"series": bandOfBrothers.Title, "season": 1}, quality+" imported", func(out map[string]any) bool {
			for _, f := range acctest.RowsOf(out["files"]) {
				if acctest.Str(f["episodes"]) == episode && acctest.Str(f["quality"]) == quality {
					got = f
					return true
				}
			}
			refreshDownloads(t)
			return false
		})

		return got
	}
	if f := fileOf("WEBDL-720p"); !acctest.BoolOf(f["below_cutoff"]) {
		t.Errorf("the file that came in = %v", f)
	}

	// 5. the audit says the file is below the profile's cutoff, and nothing
	// else of the journey's own is wrong
	below := only(t, audit(t, "audit_cutoff_unmet", nil), "series", bandOfBrothers.Title)
	if !strings.Contains(acctest.Str(below["detail"]), episode+" WEBDL-720p") || strings.Contains(acctest.Str(below["detail"]), "Upgrades Allowed off") || !strings.Contains(acctest.Str(below["fix"]), "episode_search") {
		t.Fatalf("the finding = %v", below)
	}
	journeysOwn(t, []string{tag, format, profile, tracker, client, hook, list, bandOfBrothers.Title}, "audit_cutoff_unmet")

	// 6. the search the finding names, once the better release is on the
	// tracker: it replaces the file
	if err := torrents.SetReleases(low, high); err != nil {
		t.Fatal(err)
	}
	handed(2)
	if err := fakeVideo(filepath.Join(watch, highTitle, highTitle+".mkv"), minutes, "1920x1080"); err != nil {
		t.Fatal(err)
	}
	if f := fileOf("WEBDL-1080p"); !acctest.IsBool(f["below_cutoff"], false) {
		t.Errorf("the file after the fix = %v", f)
	}
	if files := acctest.Rows(t, call(t, "file_list", map[string]any{"series": bandOfBrothers.Title, "season": 1})["files"], "files"); len(findAll(files, "episodes", episode)) != 1 {
		t.Errorf("%s has %d files after the upgrade, want the one that replaced the other", episode, len(files))
	}

	// 7. a backup, and the audits are clean of everything the journey made
	backup := acctest.Object(t, call(t, "backup_create", nil)["backup"], "backup")
	backupName := acctest.Str(backup["name"])
	if backupName == "" {
		t.Fatalf("backup_create = %v", backup)
	}
	journeysOwn(t, []string{tag, format, profile, tracker, client, hook, list, bandOfBrothers.Title}, "")
	none(t, audit(t, "audit_server", nil), "problem", "no recent backup")

	// 8. taken down with the tools, in the order that lets each go, and
	// Sonarr is as it was found
	for _, step := range teardown(bob, list, hook, client, tracker, profile, format, backupName) {
		call(t, step.tool, step.args)
	}
	call(t, "exclusion_remove", map[string]any{"exclusion": "tvdb:" + itoa(chernobyl.TvdbID)})
	call(t, "delayprofile_delete", map[string]any{"which": delayID, "confirm": true})
	call(t, "releaseprofile_delete", map[string]any{"which": "Journey Rules", "confirm": true})
	call(t, "tag_delete", map[string]any{"label": tag, "confirm": true})
	if _, err := os.Stat(filepath.Join(tvDir(), "Band of Brothers")); !os.IsNotExist(err) {
		t.Errorf("the series folder is still on disk: %v", err)
	}

	after := setupState(t)
	for _, kind := range slices.Sorted(maps.Keys(before)) {
		if before[kind] != after[kind] {
			t.Errorf("%s is not as the journey found it:\nbefore %s\nafter  %s", kind, before[kind], after[kind])
		}
	}
}

// step is one tool call of the teardown.
type step struct {
	tool string
	args map[string]any
}

// teardown is the order the journey's pieces come down in: the series
// first, with its files, then what carries the tag, then the profile and
// the format the series was on, and the backup. What takes a label is
// named by it; the exclusion, the delay and release profiles and the tag
// come after, by the caller. A backup name of "" means there is none to
// delete.
func teardown(series, list, hook, client, tracker, profile, format, backup string) []step {
	steps := []step{
		{"series_delete", map[string]any{"series": series, "delete_files": true, "confirm": true}},
		{"importlist_delete", map[string]any{"which": list, "confirm": true}},
		{"notification_delete", map[string]any{"which": hook, "confirm": true}},
		{"downloadclient_delete", map[string]any{"which": client, "confirm": true}},
		{"indexer_delete", map[string]any{"which": tracker, "confirm": true}},
		{"profile_delete", map[string]any{"profile": profile, "confirm": true}},
		{"customformat_delete", map[string]any{"which": format, "confirm": true}},
	}
	if backup != "" {
		steps = append(steps, step{"backup_delete", map[string]any{"backup": backup, "confirm": true}})
	}
	return steps
}

// journeysOwn fails for any finding, in any audit but except, that is about
// something the journey made.
func journeysOwn(t *testing.T, names []string, except string) {
	t.Helper()

	for _, name := range toolNames(t) {
		if !strings.HasPrefix(name, "audit_") || name == "audit_all" || name == except {
			continue
		}
		for _, f := range acctest.RowsOf(call(t, name, nil)["findings"]) {
			words := acctest.Str(f["series"]) + " " + acctest.Str(f["subject"]) + " " + acctest.Str(f["detail"])
			for _, own := range names {
				if strings.Contains(words, own) {
					t.Errorf("%s reports something of the journey's own: %v", name, f)
				}
			}
		}
	}
}

// setupState is what Sonarr holds of everything the journey makes, as JSON
// by kind, read through the SDK so that the tools are not trusted to say
// they left no trace.
func setupState(t *testing.T) map[string]string {
	t.Helper()

	out := map[string]string{}
	keep := func(kind string, v any, err error) {
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		out[kind] = string(raw)
	}
	tags, err := api.GetTag(ctx)
	keep("tags", tags.Model, err)
	profiles, err := api.GetQualityProfile(ctx)
	keep("quality profiles", profiles.Model, err)
	formats, err := api.GetCustomFormat(ctx)
	keep("custom formats", formats.Model, err)
	delays, err := api.GetDelayProfile(ctx)
	keep("delay profiles", delays.Model, err)
	rules, err := api.GetReleaseProfile(ctx)
	keep("release profiles", rules.Model, err)
	indexers, err := api.GetIndexer(ctx)
	keep("indexers", indexers.Model, err)
	clients, err := api.GetDownloadClient(ctx)
	keep("download clients", clients.Model, err)
	hooks, err := api.GetNotification(ctx)
	keep("notifications", hooks.Model, err)
	lists, err := api.GetImportList(ctx)
	keep("import lists", lists.Model, err)
	exclusions, err := api.GetImportListExclusionPaged(ctx, sonarr.GetImportListExclusionPagedOperationOptions{})
	keep("exclusions", exclusions.Model.Records, err)
	// a root folder's free space moves with every file written, so it is
	// named by what the journey could change
	roots, err := api.GetRootFolder(ctx)
	type root struct {
		ID         int
		Path       string
		Accessible *bool
	}
	folders := make([]root, 0, len(roots.Model))
	for _, r := range roots.Model {
		folders = append(folders, root{r.Id, r.Path, r.Accessible})
	}
	keep("root folders", folders, err)
	backups, err := api.GetSystemBackup(ctx)
	keep("backups", backups.Model, err)
	// a series is named by what the journey could change; its statistics
	// move with time and with the other tests
	series, err := api.GetSeries(ctx, sonarr.GetSeriesOperationOptions{})
	type named struct {
		ID      int
		Title   string
		Path    string
		Profile int
		Tags    []int
	}
	kept := make([]named, 0, len(series.Model))
	for i := range series.Model {
		s := &series.Model[i]
		kept = append(kept, named{s.Id, s.Title, s.Path, s.QualityProfileId, s.Tags})
	}
	keep("series", kept, err)

	return out
}
