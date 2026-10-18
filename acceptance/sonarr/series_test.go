//go:build acceptance

package sonarr_test

// The series, their episodes and their files: a series' whole life (looked
// up, imported from its folder, edited, moved on disk, deleted), the episode
// and file edits, the rename preview, the manual import, and the reads a
// library answers - the calendar, the wanted lists, the parser.

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/sonarr-mcp/lib/testenv"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// waitForFiles polls a series until Sonarr has imported want files from its
// folder: an import or an add refreshes the series from SkyHook, then scans.
func waitForFiles(ctx context.Context, t *testing.T, id sonarr.SeriesID, want int) sonarr.SeriesResource {
	t.Helper()

	var s sonarr.SeriesResource
	if !poll(2*time.Minute, func() bool {
		s = *must(sc.GetSeriesById(ctx, id)).Answer
		return s.Statistics != nil && s.Statistics.TotalEpisodeCount > 0 && s.Statistics.EpisodeFileCount == want
	}) {
		t.Fatalf("series %d never settled on %d files: %+v", id, want, s.Statistics)
	}

	return s
}

func TestSeriesLifecycle(t *testing.T) {
	ctx := skipUnlessUp(t)

	// a lookup by title answers candidates; the folder the seed left unmapped
	// is one of them
	found := must(sc.GetSeriesLookup(ctx, theExpanse.Title)).Answer
	i := slices.IndexFunc(found, func(s sonarr.SeriesResource) bool { return s.TvdbId == theExpanse.TvdbID })
	if i < 0 || found[i].Title != theExpanse.Title || found[i].Year != 2015 || len(found[i].Seasons) == 0 || found[i].Id != 0 {
		t.Fatalf("GetSeriesLookup(%s) = %d candidates, want The Expanse not yet in the library", theExpanse.Title, len(found))
	}

	// imported with its folder as its path: the files there are taken in,
	// not downloaded again
	imported := must(sc.PostSeriesImport(ctx, []sonarr.SeriesResource{forAdding(&found[i], "/tv/"+theExpanse.Folder)}))
	status(t, imported.HttpResponse, http.StatusOK)
	if len(imported.Answer) != 1 || imported.Answer[0].Id == 0 || imported.Answer[0].Path != "/tv/"+theExpanse.Folder {
		t.Fatalf("PostSeriesImport = %+v", imported.Answer)
	}
	id := imported.Answer[0].Id
	t.Cleanup(func() {
		_, _ = sc.DeleteSeriesById(context.WithoutCancel(ctx), id, sonarr.DeleteSeriesByIdOptions{})
		// the folder back where the fixtures put it, for the next run
		tv := testenv.DataDir("tv")
		if _, err := os.Stat(filepath.Join(tv, theExpanse.Folder)); os.IsNotExist(err) {
			_ = os.Rename(filepath.Join(tv, "The Expanse (2015)"), filepath.Join(tv, theExpanse.Folder))
		}
	})
	s := waitForFiles(ctx, t, id, theExpanse.Files)
	if byTvdb := must(sc.GetSeries(ctx, sonarr.GetSeriesOptions{TvdbId: theExpanse.TvdbID})).Answer; len(byTvdb) != 1 || byTvdb[0].Id != id {
		t.Errorf("GetSeries by tvdb id = %+v", byTvdb)
	}
	if s.Title != theExpanse.Title || s.Network == "" || len(s.Genres) == 0 || s.Runtime == 0 || s.Statistics.SizeOnDisk == 0 {
		t.Errorf("the imported series did not decode: %+v", s)
	}

	// the folder the naming format makes for it
	if folder := must(sc.GetSeriesByIdFolder(ctx, id)); folder.Folder != theExpanse.Title {
		t.Errorf("GetSeriesByIdFolder = %+v", folder.Answer)
	}

	// an edit, then a move: the record changes at once, the files follow
	s.Monitored = new(false)
	upd := must(sc.PutSeriesById(ctx, id, s))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if boolValue(upd.Answer.Monitored) {
		t.Errorf("PutSeriesById = %+v", upd.Answer)
	}
	moved := must(upd.Record())
	moved.Path = "/tv/The Expanse (2015)"
	status(t, must(sc.PutSeriesById(ctx, id, moved, true)).HttpResponse, http.StatusAccepted)
	if !poll(time.Minute, func() bool {
		entries, err := os.ReadDir(testenv.DataDir("tv", "The Expanse (2015)", "Season 1"))
		return err == nil && len(entries) == theExpanse.Files
	}) {
		t.Error("moving the series never moved its files")
	}

	// the season pass sets seasons' monitoring for many series at once
	pass := must(sc.PostSeasonPass(ctx, sonarr.SeasonPassResource{
		Series: []sonarr.SeasonPassSeriesResource{{Id: id, Monitored: new(true), Seasons: []sonarr.SeasonResource{{SeasonNumber: 1, Monitored: new(false)}}}},
	}))
	status(t, pass.HttpResponse, http.StatusAccepted)
	after := must(sc.GetSeriesById(ctx, id, true)).Answer
	if j := slices.IndexFunc(after.Seasons, func(se sonarr.SeasonResource) bool { return se.SeasonNumber == 1 }); j < 0 || boolValue(after.Seasons[j].Monitored) {
		t.Errorf("season 1 after the season pass = %+v", after.Seasons)
	}

	// the series editor: many series at once, answering every one it changed
	tag := newTag(ctx, t, "sdk-editor")
	edited := must(sc.PutSeriesEditor(ctx, sonarr.SeriesEditorResource{
		SeriesIds: []sonarr.SeriesID{id}, Monitored: new(true), SeriesType: sonarr.SeriesTypesStandard, Tags: []sonarr.TagID{tag}, ApplyTags: sonarr.ApplyTagsAdd,
	}))
	status(t, edited.HttpResponse, http.StatusAccepted)
	if len(edited.Answer) != 1 || !boolValue(edited.Answer[0].Monitored) || !slices.Contains(edited.Answer[0].Tags, tag) {
		t.Errorf("PutSeriesEditor = %+v", edited.Answer)
	}

	// and the editor's delete, leaving the files where they are
	status(t, must(sc.DeleteSeriesEditor(ctx, sonarr.SeriesEditorResource{SeriesIds: []sonarr.SeriesID{id}, DeleteFiles: new(false)})).HttpResponse, http.StatusOK)
	_, err := sc.GetSeriesById(ctx, id)
	gone(t, "GetSeriesById after the editor's delete", err)
	if _, err := os.Stat(testenv.DataDir("tv", "The Expanse (2015)", "Season 1")); err != nil {
		t.Errorf("the editor's delete without deleteFiles took the files: %v", err)
	}
}

// A series added with no files, and deleted with an import list exclusion,
// so an import list never adds it back.
func TestSeriesAddAndDelete(t *testing.T) {
	ctx := skipUnlessUp(t)

	show := must(lookupSeries(ctx, bandOfBrothers.TvdbID))
	res := must(sc.PostSeries(ctx, forAdding(&show, "")))
	status(t, res.HttpResponse, http.StatusCreated)
	added := must(res.Record())
	t.Cleanup(func() {
		_, _ = sc.DeleteSeriesById(context.WithoutCancel(ctx), added.Id, sonarr.DeleteSeriesByIdOptions{})
	})
	if added.Id == 0 || added.Path != "/tv/Band of Brothers" || added.QualityProfileId != profileID {
		t.Fatalf("PostSeries = %+v", added)
	}
	// the add queues a refresh from SkyHook; deleting under it would leave
	// what it fetches to chance, and a replay run short of a recording
	if !poll(time.Minute, func() bool {
		s := must(sc.GetSeriesById(ctx, added.Id)).Answer
		return s.Statistics != nil && s.Statistics.TotalEpisodeCount > 0
	}) {
		t.Fatal("the added series was never refreshed")
	}
	// the same show twice is refused
	if _, err := sc.PostSeries(ctx, forAdding(&show, "")); !isStatus(err, http.StatusBadRequest) {
		t.Errorf("adding the same show again = %v, want a 400", err)
	}
	if !slices.ContainsFunc(must(sc.GetSeries(ctx, sonarr.GetSeriesOptions{})).Answer, func(s sonarr.SeriesResource) bool { return s.Id == added.Id }) {
		t.Error("GetSeries does not list the added series")
	}

	status(t, must(sc.DeleteSeriesById(ctx, added.Id, sonarr.DeleteSeriesByIdOptions{DeleteFiles: new(true), AddImportListExclusion: new(true)})).HttpResponse, http.StatusOK)
	_, err := sc.GetSeriesById(ctx, added.Id)
	gone(t, "GetSeriesById", err)
	// the exclusion is added after the delete answers: Sonarr handles the
	// series' deletion in the background (ImportListExclusionService handles
	// SeriesDeletedEvent asynchronously), as it does the files
	var excluded sonarr.ImportListExclusionResource
	if !poll(30*time.Second, func() bool {
		all := must(sc.GetImportListExclusionPagedComplete(ctx, sonarr.GetImportListExclusionPagedOptions{})).Items
		j := slices.IndexFunc(all, func(e sonarr.ImportListExclusionResource) bool { return e.TvdbId == bandOfBrothers.TvdbID })
		if j >= 0 {
			excluded = all[j]
		}
		return j >= 0
	}) {
		t.Fatal("deleting with an import list exclusion excluded nothing")
	}
	_, _ = sc.DeleteImportListExclusionById(ctx, excluded.Id)
}

func TestEpisodes(t *testing.T) {
	ctx := skipUnlessUp(t)

	id := seriesIDs[severance.Title]
	eps := episodesOf(ctx, id)
	if len(eps) == 0 {
		t.Fatal("GetEpisode listed nothing for Severance")
	}
	e := episode(ctx, t, id, 1)
	if !boolValue(e.HasFile) || e.EpisodeFile == nil || e.EpisodeFile.Quality == nil || e.Title == "" || e.AirDateUtc.IsZero() {
		t.Errorf("S01E01 did not decode with its file: %+v", e)
	}
	if got := must(sc.GetEpisodeById(ctx, e.Id)).Answer; got.Title != e.Title {
		t.Errorf("GetEpisodeById = %+v", got)
	}
	e2 := episode(ctx, t, id, 2)
	byIDs := must(sc.GetEpisode(ctx, sonarr.GetEpisodeOptions{EpisodeIds: []sonarr.EpisodeID{e.Id, e2.Id}})).Answer
	if len(byIDs) != 2 {
		t.Errorf("GetEpisode by ids = %d episodes, want 2", len(byIDs))
	}
	byFile := must(sc.GetEpisode(ctx, sonarr.GetEpisodeOptions{EpisodeFileId: e.EpisodeFileId})).Answer
	if len(byFile) != 1 || byFile[0].Id != e.Id {
		t.Errorf("GetEpisode by file = %+v", byFile)
	}
	// an episode list with neither a series nor ids is refused
	if _, err := sc.GetEpisode(ctx, sonarr.GetEpisodeOptions{}); !isStatus(err, http.StatusBadRequest) {
		t.Errorf("GetEpisode with nothing to list = %v, want a 400", err)
	}

	e.Monitored = new(false)
	upd := must(sc.PutEpisodeById(ctx, e.Id, e))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if boolValue(upd.Answer.Monitored) {
		t.Errorf("PutEpisodeById = %+v", upd.Answer)
	}
	back := must(sc.PutEpisodeMonitor(ctx, sonarr.EpisodesMonitoredResource{EpisodeIds: []sonarr.EpisodeID{e.Id, e2.Id}, Monitored: new(true)}))
	status(t, back.HttpResponse, http.StatusAccepted)
	if len(back.Answer) != 2 || slices.ContainsFunc(back.Answer, func(x sonarr.EpisodeResource) bool { return !boolValue(x.Monitored) }) {
		t.Errorf("PutEpisodeMonitor = %+v", back.Answer)
	}
}

// Chernobyl's files are the ones edited and deleted, then written back to
// disk for a rescan to find: the rest of the suite reads them only through
// the rename preview.
func TestEpisodeFiles(t *testing.T) {
	ctx := skipUnlessUp(t)

	id := seriesIDs[chernobyl.Title]
	files := must(sc.GetEpisodeFile(ctx, sonarr.GetEpisodeFileOptions{SeriesId: id})).Answer
	if len(files) != chernobyl.Files {
		t.Fatalf("GetEpisodeFile = %d files, want %d", len(files), chernobyl.Files)
	}
	slices.SortFunc(files, func(a, b sonarr.EpisodeFileResource) int { return strings.Compare(a.RelativePath, b.RelativePath) })
	f := files[0]
	if f.Path == "" || f.Size == 0 || f.Quality == nil || f.MediaInfo == nil || f.MediaInfo.Resolution != "1920x1080" || len(f.Languages) == 0 {
		t.Errorf("an episode file did not decode: %+v", f)
	}
	if got := must(sc.GetEpisodeFileById(ctx, f.Id)).Answer; got.Path != f.Path {
		t.Errorf("GetEpisodeFileById = %+v", got)
	}
	if byIDs := must(sc.GetEpisodeFile(ctx, sonarr.GetEpisodeFileOptions{EpisodeFileIds: []sonarr.EpisodeFileID{files[0].Id, files[1].Id}})).Answer; len(byIDs) != 2 {
		t.Errorf("GetEpisodeFile by ids = %d files, want 2", len(byIDs))
	}

	// what Sonarr records about a file: its quality, languages, release group
	f.ReleaseGroup = "SDK"
	upd := must(sc.PutEpisodeFileById(ctx, f.Id, f))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if upd.Answer.ReleaseGroup != "SDK" {
		t.Errorf("PutEpisodeFileById = %+v", upd.Answer)
	}
	editor := must(sc.PutEpisodeFileEditor(ctx, sonarr.EpisodeFileListResource{EpisodeFileIds: []sonarr.EpisodeFileID{files[0].Id, files[1].Id}, ReleaseGroup: "SDK-EDITOR"}))
	status(t, editor.HttpResponse, http.StatusAccepted)
	if len(editor.Answer) != 2 || editor.Answer[0].ReleaseGroup != "SDK-EDITOR" {
		t.Errorf("PutEpisodeFileEditor = %+v", editor.Answer)
	}
	// the bulk update changes only what each entry sets: a release group
	// left out stays as it was
	bulk := must(sc.PutEpisodeFileBulk(ctx, []sonarr.EpisodeFileResource{{Id: files[1].Id, ReleaseGroup: "SDK-BULK"}, {Id: files[0].Id}}))
	status(t, bulk.HttpResponse, http.StatusAccepted)
	groups := map[sonarr.EpisodeFileID]string{}
	for _, b := range bulk.Answer {
		groups[b.Id] = b.ReleaseGroup
	}
	if groups[files[1].Id] != "SDK-BULK" || groups[files[0].Id] != "SDK-EDITOR" {
		t.Errorf("PutEpisodeFileBulk release groups = %v", groups)
	}

	// deleting a file deletes it from disk, and its episode is missing again
	status(t, must(sc.DeleteEpisodeFileById(ctx, files[3].Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetEpisodeFileById(ctx, files[3].Id)
	gone(t, "GetEpisodeFileById", err)
	if _, err := os.Stat(testenv.DataDir("tv", strings.TrimPrefix(files[3].Path, "/tv/"))); !os.IsNotExist(err) {
		t.Errorf("the deleted file is still on disk: %v", err)
	}
	status(t, must(sc.DeleteEpisodeFileBulk(ctx, sonarr.EpisodeFileListResource{EpisodeFileIds: []sonarr.EpisodeFileID{files[4].Id}})).HttpResponse, http.StatusOK)
	if left := must(sc.GetEpisodeFile(ctx, sonarr.GetEpisodeFileOptions{SeriesId: id})).Answer; len(left) != chernobyl.Files-2 {
		t.Errorf("after two deletes, %d files, want %d", len(left), chernobyl.Files-2)
	}

	// put back on disk, a rescan finds them again
	for _, f := range files[3:] {
		if err := fakeVideo(ctx, testenv.DataDir("tv", strings.TrimPrefix(f.Path, "/tv/")), 60, "1920x1080"); err != nil {
			t.Fatal(err)
		}
	}
	// a command read back carries its own fields, which the document's
	// Command schema has no place for: here the series it was for
	rescan := must(sc.RescanSeriesAndWait(ctx, sonarr.RescanSeriesCommand{SeriesId: id}, commandWait))
	if got, ok := rescan.Body["seriesId"].(float64); !ok || sonarr.SeriesID(got) != id {
		t.Errorf("RescanSeries read back for series %v, want %d: %v", rescan.Body["seriesId"], id, rescan.Body)
	}
	if back := must(sc.GetEpisodeFile(ctx, sonarr.GetEpisodeFileOptions{SeriesId: id})).Answer; len(back) != chernobyl.Files {
		t.Errorf("after the rescan, %d files, want %d", len(back), chernobyl.Files)
	}
}

// A file dropped into a download folder, read by the manual import, then
// imported by the command that takes each file's series and episodes.
func TestManualImport(t *testing.T) {
	ctx := skipUnlessUp(t)

	id := seriesIDs[severance.Title]
	dir := mkdirAll(t, testenv.DataDir(), "downloads", "manual")
	name := "Severance.S01E03.In.Perpetuity.720p.HDTV.x264-SDK.mkv"
	// eleven minutes: Sonarr takes anything shorter from a download folder
	// for a sample
	if err := fakeVideo(ctx, filepath.Join(dir, name), 11, "1280x720"); err != nil {
		t.Fatal(err)
	}
	e3 := episode(ctx, t, id, 3)

	items := must(sc.GetManualImport(ctx, sonarr.GetManualImportOptions{Folder: "/downloads/manual", FilterExistingFiles: new(true)})).Answer
	i := slices.IndexFunc(items, func(m sonarr.ManualImportResource) bool { return strings.HasSuffix(m.Path, name) })
	if i < 0 {
		t.Fatalf("GetManualImport(folder) = %+v, want %s", items, name)
	}
	item := items[i]
	if item.Series == nil || item.Series.Id != id || len(item.Episodes) != 1 || item.Episodes[0].Id != e3.Id || item.Quality == nil || item.Quality.Quality.Name != "HDTV-720p" {
		t.Errorf("GetManualImport read the file as %+v", item)
	}
	// the series form lists what the series' folder holds, already imported
	existing := must(sc.GetManualImport(ctx, sonarr.GetManualImportOptions{SeriesId: id})).Answer
	if len(existing) < severance.Files || !slices.ContainsFunc(existing, func(m sonarr.ManualImportResource) bool { return m.EpisodeFileId != 0 }) {
		t.Errorf("GetManualImport(series) = %+v", existing)
	}

	// reprocessing answers the item as it would import with what is given
	re := must(sc.PostManualImport(ctx, []sonarr.ManualImportReprocessResource{{
		Path: item.Path, SeriesId: id, SeasonNumber: 1, EpisodeIds: []sonarr.EpisodeID{e3.Id}, Quality: item.Quality, Languages: item.Languages,
	}}))
	status(t, re.HttpResponse, http.StatusOK)
	if len(re.Answer) != 1 || len(re.Answer[0].Episodes) != 1 || re.Answer[0].Episodes[0].Id != e3.Id {
		t.Errorf("PostManualImport = %+v", re.Answer)
	}

	// the import itself is a command, the files and their episodes its own
	// fields
	must(sc.ManualImportAndWait(ctx, sonarr.ManualImportCommand{ImportMode: "copy", Files: []map[string]any{{
		"path": item.Path, "folderName": item.FolderName, "seriesId": id, "episodeIds": []sonarr.EpisodeID{e3.Id},
		"quality": item.Quality, "languages": item.Languages, "releaseGroup": item.ReleaseGroup,
	}}}, commandWait))
	if got := episode(ctx, t, id, 3); !boolValue(got.HasFile) || got.EpisodeFile == nil || got.EpisodeFile.Quality.Quality.Name != "HDTV-720p" {
		t.Errorf("S01E03 after the manual import = %+v", got)
	}
}

func TestParse(t *testing.T) {
	ctx := skipUnlessUp(t)

	p := must(sc.GetParse(ctx, sonarr.GetParseOptions{Title: "Firefly.S01E02.Bushwhacked.1080p.WEB-DL.DD5.1.H.264-GRP"})).Answer
	if p.Series == nil || p.Series.Id != seriesIDs[firefly.Title] || p.ParsedEpisodeInfo == nil || !slices.Equal(p.ParsedEpisodeInfo.EpisodeNumbers, []int{2}) ||
		p.ParsedEpisodeInfo.Quality.Quality.Name != "WEBDL-1080p" || p.ParsedEpisodeInfo.ReleaseGroup != "GRP" || len(p.Episodes) != 1 {
		t.Errorf("GetParse = %+v", p)
	}
	// a name Sonarr cannot read is an answer with nothing parsed, not an error
	none := must(sc.GetParse(ctx, sonarr.GetParseOptions{Title: "holiday photos"})).Answer
	if none.ParsedEpisodeInfo != nil || none.Series != nil {
		t.Errorf("GetParse(holiday photos) = %+v", none)
	}
}

func TestCalendarAndWanted(t *testing.T) {
	ctx := skipUnlessUp(t)

	// Firefly's first run, which aired in 2002
	cal := must(sc.GetCalendar(ctx, sonarr.GetCalendarOptions{
		Start: day(2002, time.September, 1), End: day(2002, time.December, 31), Unmonitored: new(true), IncludeSeries: new(true),
	})).Answer
	i := slices.IndexFunc(cal, func(e sonarr.EpisodeResource) bool { return e.SeriesId == seriesIDs[firefly.Title] })
	if i < 0 || cal[i].Series == nil || cal[i].Series.Title != firefly.Title {
		t.Fatalf("GetCalendar for late 2002 = %d episodes, want Firefly's", len(cal))
	}
	if got := must(sc.GetCalendarById(ctx, cal[i].Id)).Answer; got.Id != cal[i].Id {
		t.Errorf("GetCalendarById = %+v", got)
	}

	missing := must(sc.GetWantedMissingComplete(ctx, sonarr.GetWantedMissingOptions{Monitored: new(true), IncludeSeries: new(true), PageSize: new(10)})).Items
	j := slices.IndexFunc(missing, func(e sonarr.EpisodeResource) bool {
		return e.SeriesId == seriesIDs[firefly.Title] && e.SeasonNumber == 1 && e.EpisodeNumber == 7
	})
	if j < 0 || boolValue(missing[j].HasFile) {
		t.Fatalf("GetWantedMissingComplete over pages of ten = %d episodes, want Firefly S01E07", len(missing))
	}
	if got := must(sc.GetWantedMissingById(ctx, missing[j].Id)).Answer; got.Id != missing[j].Id {
		t.Errorf("GetWantedMissingById = %+v", got)
	}

	// Firefly's SDTV files are below the HD-1080p profile's cutoff
	cutoff := must(sc.GetWantedCutoffComplete(ctx, sonarr.GetWantedCutoffOptions{Monitored: new(true), IncludeEpisodeFile: new(true)})).Items
	k := slices.IndexFunc(cutoff, func(e sonarr.EpisodeResource) bool {
		return e.SeriesId == seriesIDs[firefly.Title] && e.EpisodeNumber == 6
	})
	if k < 0 || cutoff[k].EpisodeFile == nil || cutoff[k].EpisodeFile.Quality.Quality.Name != "SDTV" || !boolValue(cutoff[k].EpisodeFile.QualityCutoffNotMet) {
		t.Fatalf("GetWantedCutoffComplete = %d episodes, want Firefly S01E06 in SDTV", len(cutoff))
	}
	if got := must(sc.GetWantedCutoffById(ctx, cutoff[k].Id)).Answer; got.Id != cutoff[k].Id {
		t.Errorf("GetWantedCutoffById = %+v", got)
	}

	// the iCal feed of the same, a file
	feed := must(sc.GetFeedV3CalendarSonarrIcs(ctx, sonarr.GetFeedV3CalendarSonarrIcsOptions{PastDays: new(9000), FutureDays: new(30), Unmonitored: new(true)}))
	defer func() { _ = feed.HttpResponse.Body.Close() }()
	body, err := io.ReadAll(feed.HttpResponse.Body)
	if err != nil || !strings.HasPrefix(string(body), "BEGIN:VCALENDAR") || !strings.Contains(string(body), "Firefly") {
		t.Errorf("GetFeedV3CalendarSonarrIcs = %.200s, %v", body, err)
	}
}
