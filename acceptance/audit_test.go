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

// The audits against the library as seeded: each finds what its fixture
// planted, and leaves the rest alone. These run first (the files sort
// first), before any test changes the library; the ones that need a defect
// the seed does not hold make it, and undo it.

// audit calls an audit and returns its answer, logging the findings so a
// failing run shows what the audit did find.
func audit(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()

	out := call(t, name, args)
	for _, f := range rowsOf(out["findings"]) {
		t.Logf("%s: %s | %s | %s | %s", name, str(f["series"]), str(f["subject"]), str(f["problem"]), str(f["detail"]))
	}

	return out
}

// only fails unless exactly one finding matches the filter, and returns it.
func only(t *testing.T, out map[string]any, filter ...string) map[string]any {
	t.Helper()

	match := findings(t, out, filter...)
	if len(match) != 1 {
		t.Fatalf("%d findings match %v, want 1", len(match), filter)
	}

	return match[0]
}

// none fails when any finding matches the filter.
func none(t *testing.T, out map[string]any, filter ...string) {
	t.Helper()

	if match := findings(t, out, filter...); len(match) > 0 {
		t.Errorf("%d findings match %v, want none: %v", len(match), filter, match)
	}
}

func TestAuditMissingEpisodes(t *testing.T) {
	out := audit(t, "audit_missing_episodes", nil)

	// Firefly holds E01-E06 of fourteen: the rest of the season is missing,
	// which is a gap, not a whole season
	f := only(t, out, "series", firefly.Title, "subject", "season 1")
	if str(f["problem"]) != "episodes missing" || !strings.Contains(str(f["detail"]), "8 missing: S01E07") {
		t.Errorf("Firefly = %v", f)
	}
	// Breaking Bad has no files, so its seasons are missing whole
	if bb := findings(t, out, "series", breakingBad.Title, "problem", "whole season missing"); len(bb) < 5 {
		t.Errorf("Breaking Bad whole seasons missing = %d, want its 5 seasons: %v", len(bb), bb)
	}
	// Chernobyl is complete, and the specials of every series are
	// unmonitored, so neither is missing anything
	none(t, out, "series", chernobyl.Title)
	none(t, out, "subject", "season 0")
	if num(t, out["total_findings"], "total_findings") != len(rows(t, out["findings"], "findings")) {
		t.Error("total_findings disagrees with the findings under the default limit")
	}

	// one series, and the limit caps the list but not the count
	one := audit(t, "audit_missing_episodes", map[string]any{"series": breakingBad.Title, "limit": 2})
	if len(rows(t, one["findings"], "findings")) != 2 || num(t, one["total_findings"], "total_findings") < 5 {
		t.Errorf("limit 2 of Breaking Bad = %v", one)
	}
	none(t, one, "series", firefly.Title)
}

func TestAuditCutoffUnmet(t *testing.T) {
	out := audit(t, "audit_cutoff_unmet", nil)

	// E04's video is 360p and E06 is SDTV: both below HD-1080p's cutoff
	f := only(t, out, "series", firefly.Title)
	for _, want := range []string{"2 below HD-1080p", "S01E04 SDTV", "S01E06 SDTV"} {
		if !strings.Contains(str(f["detail"]), want) {
			t.Errorf("Firefly detail %q lacks %q", f["detail"], want)
		}
	}
	for _, s := range []seriesFixture{chernobyl, cowboyBebop, severance} {
		none(t, out, "series", s.Title)
	}
}

func TestAuditUnmappedFolders(t *testing.T) {
	out := audit(t, "audit_unmapped_folders", nil)

	f := only(t, out, "subject", "/tv/"+theExpanse.Folder)
	if str(f["problem"]) != "folder not in Sonarr" || !strings.Contains(str(f["fix"]), "series_import") {
		t.Errorf("The Expanse = %v", f)
	}
	// what is in it, so a model can tell a show from an empty folder
	if !strings.Contains(str(f["detail"]), strconv.Itoa(theExpanse.Files)+" video files") {
		t.Errorf("The Expanse detail = %v", f["detail"])
	}
	// a series' own folder is mapped
	none(t, out, "subject", "/tv/"+firefly.Folder)
}

// Every seeded series' folder is where Sonarr thinks it is.
func TestAuditMissingFolders(t *testing.T) {
	out := audit(t, "audit_missing_folders", nil)

	if n := num(t, out["total_findings"], "total_findings"); n != 0 {
		t.Errorf("audit_missing_folders found %d, want none", n)
	}
	if n := num(t, out["scanned"], "scanned"); n < len(importedSeed) {
		t.Errorf("audit_missing_folders scanned %d series", n)
	}

	// a folder renamed to something no show goes by: the series has lost it,
	// and nothing on disk says where it went
	skipUnlessReady(t)
	from := filepath.Join(tvDir(), chernobyl.Folder)
	to := filepath.Join(tvDir(), "zzz-not-a-show")
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
	gone := audit(t, "audit_missing_folders", map[string]any{"series": chernobyl.Title})
	if err := os.Rename(to, from); err != nil {
		t.Fatal(err)
	}
	f := only(t, gone, "series", chernobyl.Title)
	if str(f["problem"]) != "series folder missing" || !strings.Contains(str(f["detail"]), "records 5 files there") ||
		!strings.Contains(str(f["fix"]), "series_edit path") {
		t.Errorf("the folder that went = %v", f)
	}
}

func TestAuditNaming(t *testing.T) {
	out := audit(t, "audit_naming", nil)

	// Chernobyl's files carry their scene names
	f := only(t, out, "series", chernobyl.Title)
	if !strings.Contains(str(f["detail"]), "5 of 5 files") || !strings.Contains(str(f["detail"]), "Chernobyl.S01E0") {
		t.Errorf("Chernobyl = %v", f)
	}
	// Severance's are named to the format already
	none(t, out, "series", severance.Title)

	// with renaming off Sonarr's preview is empty whatever the names are, so
	// the audit says renaming is off rather than that all is well
	if err := setRenaming(false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := setRenaming(true); err != nil {
			t.Error(err)
		}
	})
	off := audit(t, "audit_naming", nil)
	if f := only(t, off, "problem", "renaming is off"); !strings.Contains(str(f["fix"]), "Rename Episodes") {
		t.Errorf("renaming off = %v", f)
	}
	if n := num(t, off["total_findings"], "total_findings"); n != 1 {
		t.Errorf("renaming off found %d, want the one", n)
	}
	// and series_rename will not pretend there is nothing to do
	if msg := callErr(t, "series_rename", map[string]any{"series": chernobyl.Title, "dry_run": true}); !strings.Contains(msg, "Rename Episodes setting is off") {
		t.Errorf("series_rename with renaming off = %s", msg)
	}
}

func TestAuditMonitoring(t *testing.T) {
	out := audit(t, "audit_monitoring", nil)

	f := only(t, out, "series", severance.Title)
	if str(f["problem"]) != "new seasons will not be monitored" || !strings.Contains(str(f["fix"]), "monitor_new_items all") {
		t.Errorf("Severance = %v", f)
	}
	// the ended series have no new seasons to miss
	for _, s := range []seriesFixture{firefly, chernobyl, breakingBad} {
		none(t, out, "series", s.Title)
	}

	// a continuing series nobody monitors will never grab anything
	call(t, "series_edit", map[string]any{"series": severance.Title, "monitored": false})
	t.Cleanup(func() { call(t, "series_edit", map[string]any{"series": severance.Title, "monitored": true}) })
	off := audit(t, "audit_monitoring", nil)
	if f := only(t, off, "series", severance.Title); str(f["problem"]) != "continuing series not monitored" ||
		!strings.Contains(str(f["fix"]), "series_edit monitored true") {
		t.Errorf("Severance unmonitored = %v", f)
	}
	call(t, "series_edit", map[string]any{"series": severance.Title, "monitored": true})

	// and a monitored series with every season turned off searches for
	// nothing, which no other finding would say
	var seasons []any
	for _, s := range rows(t, call(t, "series_get", map[string]any{"series": severance.Title})["seasons"], "seasons") {
		if n := num(t, s["season"], "season"); n > 0 {
			seasons = append(seasons, n)
		}
	}
	call(t, "season_monitor", map[string]any{"series": severance.Title, "seasons": seasons, "monitored": false})
	t.Cleanup(func() {
		call(t, "season_monitor", map[string]any{"series": severance.Title, "seasons": seasons, "monitored": true})
	})
	none := audit(t, "audit_monitoring", nil)
	if f := only(t, none, "series", severance.Title); str(f["problem"]) != "nothing monitored" ||
		!strings.Contains(str(f["fix"]), "season_monitor") {
		t.Errorf("Severance with no season monitored = %v", f)
	}
}

func TestAuditSeriesSettings(t *testing.T) {
	out := audit(t, "audit_series_settings", nil)

	anime := only(t, out, "series", cowboyBebop.Title)
	if str(anime["problem"]) != "anime not typed anime" || !strings.Contains(str(anime["fix"]), "series_type anime") {
		t.Errorf("Cowboy Bebop = %v", anime)
	}
	folder := only(t, out, "series", chernobyl.Title)
	if str(folder["problem"]) != "folder not named to the format" || !strings.Contains(str(folder["detail"]), `"Chernobyl"`) {
		t.Errorf("Chernobyl = %v", folder)
	}
	none(t, out, "series", firefly.Title)
	none(t, out, "problem", "outside every root folder")
}

func TestAuditProfiles(t *testing.T) {
	out := audit(t, "audit_profiles", nil)

	// every series is on HD-1080p, so the other default profiles are unused
	none(t, out, "subject", profile, "problem", "quality profile unused")
	if unused := findings(t, out, "problem", "quality profile unused"); len(unused) == 0 {
		t.Error("no unused quality profile reported; Sonarr's other defaults are all unused")
	}
	tag := only(t, out, "subject", unusedTag)
	if str(tag["problem"]) != "tag unused" || str(tag["fix"]) != "tag_delete "+unusedTag {
		t.Errorf("the unused tag = %v", tag)
	}
	// anime is on Cowboy Bebop
	none(t, out, "subject", "anime")

	// a release profile switched off does nothing at all
	off, err := api.PostReleaseProfile(ctx, sonarr.ReleaseProfileResource{
		Name: "Test switched off", Enabled: new(false), Required: []string{"x264"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = api.DeleteReleaseProfileById(ctx, off.Model.Id) })

	// and a tag that scopes a setting but no series scopes nothing
	call(t, "tag_create", map[string]any{"label": scopingTag})
	// the test deletes it as the fix; the cleanup is for a run that stops first
	t.Cleanup(func() { _, _ = invoke("tag_delete", map[string]any{"label": scopingTag}) })
	tagID := num(t, findRow(t, rows(t, call(t, "tag_list", nil)["tags"], "tags"), "label", scopingTag)["id"], "id")
	indexers, err := api.GetIndexer(ctx)
	if err != nil || len(indexers.Model) == 0 {
		t.Fatalf("reading the indexers: %v", err)
	}
	indexer := indexers.Model[0]
	indexer.Tags = []int{tagID}
	if _, err := api.PutIndexerById(ctx, indexer.Id, indexer, sonarr.PutIndexerByIdOperationOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		indexer.Tags = nil
		_, _ = api.PutIndexerById(ctx, indexer.Id, indexer, sonarr.PutIndexerByIdOperationOptions{})
	})

	again := audit(t, "audit_profiles", nil)
	if f := only(t, again, "subject", "Test switched off"); str(f["problem"]) != "release profile disabled" {
		t.Errorf("the disabled release profile = %v", f)
	}
	scoped := only(t, again, "subject", scopingTag)
	if str(scoped["problem"]) != "tag scopes settings but no series" || !strings.Contains(str(scoped["detail"]), "indexer") {
		t.Errorf("the tag that scopes an indexer = %v", scoped)
	}

	// the fix the findings name: take the tag off the indexer and delete it,
	// and the audit has nothing more to say about it
	indexer.Tags = nil
	if _, err := api.PutIndexerById(ctx, indexer.Id, indexer, sonarr.PutIndexerByIdOperationOptions{}); err != nil {
		t.Fatal(err)
	}
	call(t, "tag_delete", map[string]any{"label": scopingTag})
	if left := findings(t, audit(t, "audit_profiles", nil), "subject", scopingTag); len(left) != 0 {
		t.Errorf("after deleting the tag = %v", left)
	}
}

func TestAuditRuntime(t *testing.T) {
	out := audit(t, "audit_runtime", nil)

	f := only(t, out, "series", firefly.Title)
	if !strings.HasPrefix(str(f["subject"]), "S01E05 ") || str(f["problem"]) != "shorter than it should be" || !strings.Contains(str(f["detail"]), "runs 5 minutes") {
		t.Errorf("Firefly E05 = %v", f)
	}

	// a tolerance wide enough takes it back out
	loose := audit(t, "audit_runtime", map[string]any{"series": firefly.Title, "tolerance_percent": 95})
	none(t, loose, "series", firefly.Title)
}

func TestAuditLanguage(t *testing.T) {
	out := audit(t, "audit_language", nil)

	// E05 is the German dub, with a German audio track
	f := only(t, out, "series", chernobyl.Title)
	if !strings.HasPrefix(str(f["subject"]), "S01E05 ") || str(f["problem"]) != "no audio in the language" || !strings.Contains(str(f["detail"]), "ger") {
		t.Errorf("Chernobyl E05 = %v", f)
	}
	// Cowboy Bebop is in its original Japanese
	none(t, out, "series", cowboyBebop.Title)

	// asked about Japanese instead, every English series is out of it; the
	// untagged audio of the others is not taken as proof either way
	jpn := audit(t, "audit_language", map[string]any{"series": cowboyBebop.Title, "language": "English"})
	if len(findings(t, jpn, "problem", "no audio in the language")) != 2 {
		t.Errorf("Cowboy Bebop against English = %v", jpn["findings"])
	}

	// every fixture file has a language on its audio track, so a file
	// recorded in one language with nothing to say either way is the
	// unit tests' business (TestAuditLanguageEdges); what the library can
	// show is the fix: recorded French, the audio still English
	e01 := fireflyFile(t, 1)
	t.Cleanup(func() { call(t, "file_edit", map[string]any{"file_ids": []any{e01}, "languages": []any{"English"}}) })
	call(t, "file_edit", map[string]any{"file_ids": []any{e01}, "languages": []any{"French"}})
	if f := findings(t, audit(t, "audit_language", map[string]any{"series": firefly.Title, "language": "French"}), "problem", "no audio in the language"); len(f) != 1 {
		t.Errorf("recorded French with English audio, asked about French = %v", f)
	}
	call(t, "file_edit", map[string]any{"file_ids": []any{e01}, "languages": []any{"English"}})
	if f := findings(t, audit(t, "audit_language", map[string]any{"series": firefly.Title}), "series", firefly.Title); len(f) != 0 {
		t.Errorf("after putting the language back = %v", f)
	}
}

func TestAuditQualityMismatch(t *testing.T) {
	// Sonarr read E04's quality from its 360p video, so nothing is mislabelled
	// until someone records it as what its name says
	before := audit(t, "audit_quality_mismatch", nil)
	none(t, before, "series", firefly.Title)

	e04 := fireflyFile(t, 4)
	call(t, "file_edit", map[string]any{"file_ids": []any{e04}, "quality": "HDTV-1080p"})
	t.Cleanup(func() { call(t, "file_edit", map[string]any{"file_ids": []any{e04}, "quality": "SDTV"}) })

	out := audit(t, "audit_quality_mismatch", map[string]any{"series": firefly.Title})
	f := only(t, out, "series", firefly.Title)
	if str(f["problem"]) != "labelled better than it is" || !strings.Contains(str(f["detail"]), "640x360") || !strings.Contains(str(f["fix"]), "file_edit file "+strconv.Itoa(e04)) {
		t.Errorf("E04 recorded as 1080p = %v", f)
	}

	// the other way round: an HD file recorded as SD, which Sonarr would
	// keep trying to upgrade for ever
	e01 := fireflyFile(t, 1)
	call(t, "file_edit", map[string]any{"file_ids": []any{e01}, "quality": "SDTV"})
	t.Cleanup(func() { call(t, "file_edit", map[string]any{"file_ids": []any{e01}, "quality": "WEBDL-1080p"}) })
	worse := only(t, audit(t, "audit_quality_mismatch", map[string]any{"series": firefly.Title}), "problem", "labelled worse than it is")
	if !strings.Contains(str(worse["detail"]), "1920x1080") || !strings.Contains(str(worse["fix"]), "stops trying to upgrade") {
		t.Errorf("E01 recorded as SDTV = %v", worse)
	}
}

func TestAuditUntrackedAndMissingFiles(t *testing.T) {
	skipUnlessReady(t)

	// a file dropped into the series folder that Sonarr has not scanned, and
	// one whose name says nothing about which episode it is
	season := filepath.Join(tvDir(), firefly.Folder, "Season 1")
	dropped := filepath.Join(season, "Firefly.S01E07.Safe.1080p.WEB-DL.DD5.1.H.264-FAKE.mkv")
	mystery := filepath.Join(season, "bonus footage.mkv")
	// and a worse copy of an episode already there, which Sonarr refuses
	worse := filepath.Join(season, "Firefly.S01E01.The.Train.Job.480p.HDTV.x264-FAKE.mkv")
	for _, p := range []string{dropped, mystery, worse} {
		if err := fakeVideo(p, 45, "1920x1080"); err != nil {
			t.Fatal(err)
		}
	}
	// and a file Sonarr records, deleted behind its back
	e03 := fireflyPath(t, 3)
	if err := os.Rename(hostPath(e03), hostPath(e03)+".away"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(dropped)
		_ = os.Remove(mystery)
		_ = os.Remove(worse)
		_ = os.Rename(hostPath(e03)+".away", hostPath(e03))
	})

	untracked := audit(t, "audit_untracked_files", map[string]any{"series": firefly.Title})
	ready := only(t, untracked, "subject", "Season 1/Firefly.S01E07")
	if str(ready["problem"]) != "not imported" || !strings.Contains(str(ready["detail"]), "S01E07") {
		t.Errorf("the dropped file = %v", ready)
	}
	odd := only(t, untracked, "subject", "Season 1/bonus footage.mkv")
	if str(odd["problem"]) != "cannot tell which episode" {
		t.Errorf("the unnamed file = %v", odd)
	}
	spare := only(t, untracked, "subject", "Season 1/Firefly.S01E01")
	if str(spare["problem"]) != "rejected" || !strings.Contains(str(spare["detail"]), "Sonarr says:") ||
		!strings.Contains(str(spare["fix"]), "import_apply to import it anyway") {
		t.Errorf("the worse copy = %v", spare)
	}

	missing := audit(t, "audit_missing_files", map[string]any{"series": firefly.Title})
	gone := only(t, missing, "problem", "files gone from disk")
	if !strings.Contains(str(gone["detail"]), "S01E03") {
		t.Errorf("the deleted file = %v", gone)
	}

	// the fix the finding names: import the file Sonarr was ready to take,
	// and the audit stops reporting it
	call(t, "import_apply", map[string]any{"files": []any{map[string]any{"path": "/tv/" + firefly.Folder + "/Season 1/" + filepath.Base(dropped)}}})
	eventually(t, "audit_untracked_files", map[string]any{"series": firefly.Title}, "the imported file to leave the audit", func(out map[string]any) bool {
		return len(findings(t, out, "subject", "Season 1/Firefly.S01E07")) == 0
	})
	t.Cleanup(func() {
		for _, f := range rowsOf(call(t, "file_list", map[string]any{"series": firefly.Title, "season": 1})["files"]) {
			if str(f["episodes"]) == "S01E07" {
				call(t, "file_delete", map[string]any{"file_ids": []any{numOr0(f["id"])}})
			}
		}
	})

	// a folder that is there and empty, which is not the same as a folder
	// that has gone
	bebop := filepath.Join(tvDir(), cowboyBebop.Folder)
	aside := filepath.Join(tvDir(), ".aside")
	if err := os.Rename(filepath.Join(bebop, "Season 1"), aside); err != nil {
		t.Fatal(err)
	}
	empty := only(t, audit(t, "audit_missing_files", map[string]any{"series": cowboyBebop.Title}), "problem", "folder holds no files")
	if err := os.Rename(aside, filepath.Join(bebop, "Season 1")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(str(empty["detail"]), "records 2 files") || !strings.Contains(str(empty["fix"]), "series_rescan") {
		t.Errorf("the empty folder = %v", empty)
	}

	// the whole library sees the same, and nothing more: the two left after
	// the import above, and no other series
	all := audit(t, "audit_untracked_files", nil)
	if n := len(findings(t, all, "series", firefly.Title)); n != 2 {
		t.Errorf("library-wide untracked for Firefly = %d, want 2", n)
	}
	if n := len(rowsOf(all["findings"])); n != 2 {
		t.Errorf("library-wide untracked = %d findings, want Firefly's 2", n)
	}
}

func TestAuditHealth(t *testing.T) {
	out := audit(t, "audit_health", nil)

	// the suite turns RSS off on its indexer, so nothing grabs behind a
	// test's back, and Sonarr rightly calls that an error: with no indexer
	// on RSS it grabs nothing new on its own. Nothing else is wrong
	rss := only(t, out, "subject", "IndexerRssCheck")
	if str(rss["problem"]) != "health error" || !strings.HasPrefix(str(rss["fix"]), "see https://wiki.servarr.com/") {
		t.Errorf("the RSS check = %v", rss)
	}
	if errs := findings(t, out, "problem", "health error"); len(errs) != 1 {
		t.Errorf("health errors = %v, want only the RSS one", errs)
	}
	none(t, out, "problem", "root folder unreachable")
	if num(t, out["scanned"], "scanned") < 1 {
		t.Errorf("scanned = %v, want at least the root folder", out["scanned"])
	}
}

// Every audit_all row is the count its own audit reports, so the overview
// never names a number the worklist cannot reproduce.
func TestAuditAll(t *testing.T) {
	out := call(t, "audit_all", nil)

	auditRows := rows(t, out["audits"], "audits")
	names := make([]string, 0, len(auditRows))
	total := 0
	for _, row := range auditRows {
		names = append(names, str(row["audit"]))
		total += num(t, row["findings"], "findings")
	}
	if total != num(t, out["total_findings"], "total_findings") {
		t.Errorf("rows add to %d, total_findings says %v", total, out["total_findings"])
	}
	// every audit tool is in the overview
	for _, name := range toolNames(t) {
		if strings.HasPrefix(name, "audit_") && name != "audit_all" && !slices.Contains(names, name) {
			t.Errorf("%s is not in audit_all", name)
		}
	}
	for _, name := range []string{"audit_missing_episodes", "audit_naming", "audit_runtime", "audit_language"} {
		row := findRow(t, auditRows, "audit", name)
		own := call(t, name, nil)
		if num(t, row["findings"], name) != num(t, own["total_findings"], name) {
			t.Errorf("audit_all says %s has %v, the audit says %v", name, row["findings"], own["total_findings"])
		}
	}

	// and for one series, only that series
	one := call(t, "audit_all", map[string]any{"series": chernobyl.Title})
	if num(t, one["series"], "series") != 1 {
		t.Errorf("audit_all for Chernobyl covered %v series", one["series"])
	}
	for _, row := range rows(t, one["audits"], "audits") {
		if str(row["audit"]) == "audit_missing_episodes" && num(t, row["findings"], "findings") != 0 {
			t.Errorf("Chernobyl is complete, but audit_all counts %v missing", row["findings"])
		}
	}
}

// fireflyFile is the id of Firefly's file for an episode of season 1.
func fireflyFile(t *testing.T, episode int) int {
	t.Helper()

	out := call(t, "file_list", map[string]any{"series": firefly.Title, "season": 1})
	f := findRow(t, rows(t, out["files"], "files"), "episodes", episodeName(1, episode))

	return num(t, f["id"], "id")
}

// fireflyPath is where Sonarr records Firefly's file for an episode of
// season 1.
func fireflyPath(t *testing.T, episode int) string {
	t.Helper()

	out := call(t, "file_list", map[string]any{"series": firefly.Title, "season": 1})
	f := findRow(t, rows(t, out["files"], "files"), "episodes", episodeName(1, episode))

	return str(out["path"]) + "/" + str(f["relative_path"])
}

func episodeName(season, episode int) string {
	return "S" + pad(season) + "E" + pad(episode)
}

func pad(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}

	return strconv.Itoa(n)
}

// toolNames lists every tool the server registered.
func toolNames(t *testing.T) []string {
	t.Helper()

	skipUnlessReady(t)
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}

	return out
}
