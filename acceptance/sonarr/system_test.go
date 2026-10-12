//go:build acceptance

package sonarr_test

// Sonarr about itself: its status, scheduled tasks, backups, logs, updates,
// health and disks, the file system as it sees it, the languages and
// translations it offers, the commands it runs, the posters it keeps, and
// the unversioned endpoints outside /api/v3.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

func TestSystemStatus(t *testing.T) {
	ctx := skipUnlessUp(t)

	st := must(sc.GetSystemStatus(ctx)).Answer
	if !strings.HasPrefix(st.Version, "4.") || st.AppName != "Sonarr" || st.OsName == "" || st.StartTime == "" || !st.IsDocker {
		t.Errorf("GetSystemStatus = %+v", st)
	}

	// outside /api/v3: the API versions, and the ping a load balancer uses
	var api struct {
		Current    string   `json:"current"`
		Deprecated []string `json:"deprecated"`
	}
	if err := unmarshal(must(sc.GetApi(ctx)).Answer, &api); err != nil || api.Current != "v3" {
		t.Errorf("GetApi = %+v, %v", api, err)
	}
	if ping := must(sc.GetPing(ctx)).Answer; ping.Status != "OK" {
		t.Errorf("GetPing = %+v", ping)
	}
}

func TestSystemTasks(t *testing.T) {
	ctx := skipUnlessUp(t)

	tasks := must(sc.GetSystemTask(ctx)).Answer
	i := slices.IndexFunc(tasks, func(x sonarr.TaskResource) bool { return x.TaskName == "RefreshMonitoredDownloads" })
	if i < 0 || tasks[i].Interval != 1 || tasks[i].NextExecution == "" {
		t.Fatalf("GetSystemTask = %+v, want the download check every minute", tasks)
	}
	if got := must(sc.GetSystemTaskById(ctx, tasks[i].Id)).Answer; got.TaskName != tasks[i].TaskName {
		t.Errorf("GetSystemTaskById = %+v", got)
	}
}

// A backup is made by the Backup command, listed, and deleted.
func TestBackups(t *testing.T) {
	ctx := skipUnlessUp(t)

	before := must(sc.GetSystemBackup(ctx)).Answer
	must(sc.BackupAndWait(ctx, sonarr.BackupCommand{}, commandWait))
	after := must(sc.GetSystemBackup(ctx)).Answer
	i := slices.IndexFunc(after, func(b sonarr.BackupResource) bool {
		return !slices.ContainsFunc(before, func(o sonarr.BackupResource) bool { return o.Id == b.Id })
	})
	if i < 0 {
		t.Fatalf("the Backup command made no backup: %+v", after)
	}
	b := after[i]
	if b.Type != sonarr.BackupTypeManual || !strings.HasSuffix(b.Name, ".zip") || b.Size == 0 || b.Time == "" {
		t.Errorf("the new backup = %+v", b)
	}

	status(t, must(sc.DeleteSystemBackupById(ctx, b.Id)).HttpResponse, http.StatusOK)
	if slices.ContainsFunc(must(sc.GetSystemBackup(ctx)).Answer, func(x sonarr.BackupResource) bool { return x.Id == b.Id }) {
		t.Error("the deleted backup is still listed")
	}
}

func TestLogs(t *testing.T) {
	ctx := skipUnlessUp(t)

	page := must(sc.GetLog(ctx, sonarr.GetLogOptions{PageSize: new(5), SortKey: "time", SortDirection: sonarr.SortDirectionDescending})).Answer
	if len(page.Records) != 5 || page.TotalRecords < 5 || page.Records[0].Message == "" || page.Records[0].Level == "" {
		t.Errorf("GetLog, five newest = %+v", page)
	}

	files := must(sc.GetLogFile(ctx)).Answer
	i := slices.IndexFunc(files, func(f sonarr.LogFileResource) bool { return f.Filename == "sonarr.txt" })
	if i < 0 || files[i].LastWriteTime == "" || files[i].ContentsUrl == "" {
		t.Fatalf("GetLogFile = %+v, want sonarr.txt", files)
	}
	res := must(sc.GetLogFileByFilename(ctx, files[i].Filename))
	defer func() { _ = res.HttpResponse.Body.Close() }()
	text, err := io.ReadAll(io.LimitReader(res.HttpResponse.Body, 1<<20))
	if err != nil || !bytes.Contains(text, []byte("|Info|")) {
		t.Errorf("GetLogFileByFilename(sonarr.txt) = %.200s, %v", text, err)
	}
	// a container is updated by replacing its image, so there are no update
	// logs, but the list answers
	if updates := must(sc.GetLogFileUpdate(ctx)).Answer; len(updates) != 0 {
		t.Logf("GetLogFileUpdate lists %d files", len(updates))
	}
}

func TestUpdatesHealthAndDisks(t *testing.T) {
	ctx := skipUnlessUp(t)

	// the recent releases, from Sonarr's update service (through the proxy)
	for _, u := range must(sc.GetUpdate(ctx)).Answer {
		if u.Version == "" || u.ReleaseDate == "" {
			t.Errorf("an update = %+v", u)
		}
	}
	for _, h := range must(sc.GetHealth(ctx)).Answer {
		if h.Source == "" || h.Message == "" {
			t.Errorf("a health check = %+v", h)
		}
	}
	// the disks the root folders are on, which in a container may be the
	// root file system alone: Sonarr leaves out mounts of the kinds it does
	// not count as disks, which is how a VM shares a host folder
	disks := must(sc.GetDiskSpace(ctx)).Answer
	if len(disks) == 0 || slices.ContainsFunc(disks, func(d sonarr.DiskSpaceResource) bool { return d.Path == "" || d.TotalSpace < d.FreeSpace }) {
		t.Errorf("GetDiskSpace = %+v", disks)
	}
}

// The file system as Sonarr sees it, which is what its folder pickers show.
func TestFileSystem(t *testing.T) {
	ctx := skipUnlessUp(t)

	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Type string `json:"type"`
	}
	var listing struct {
		Directories []entry `json:"directories"`
	}
	if err := unmarshal(must(sc.GetFileSystem(ctx, sonarr.GetFileSystemOptions{Path: "/tv/"})).Answer, &listing); err != nil ||
		!slices.ContainsFunc(listing.Directories, func(d entry) bool { return d.Name == firefly.Folder && d.Type == "folder" }) {
		t.Errorf("GetFileSystem(/tv/) = %+v, %v", listing, err)
	}

	var media []entry
	if err := unmarshal(must(sc.GetFileSystemMediaFiles(ctx, sonarr.GetFileSystemMediaFilesOptions{Path: "/tv/" + firefly.Folder})).Answer, &media); err != nil ||
		len(media) < firefly.Files || !strings.HasSuffix(media[0].Name, ".mkv") {
		t.Fatalf("GetFileSystemMediaFiles = %+v, %v", media, err)
	}

	// a file is a file; anything else is answered as a folder, so the
	// picker gives nothing away about what is not there
	for path, want := range map[string]string{media[0].Path: "file", "/tv": "folder", "/nowhere": "folder"} {
		var kind struct {
			Type string `json:"type"`
		}
		if err := unmarshal(must(sc.GetFileSystemType(ctx, sonarr.GetFileSystemTypeOptions{Path: path})).Answer, &kind); err != nil || kind.Type != want {
			t.Errorf("GetFileSystemType(%s) = %+v, %v, want %s", path, kind, err, want)
		}
	}
}

func TestLanguagesAndLocalization(t *testing.T) {
	ctx := skipUnlessUp(t)

	langs := must(sc.GetLanguage(ctx)).Answer
	if !slices.ContainsFunc(langs, func(l sonarr.LanguageResource) bool { return l.Id == 1 && l.Name == "English" }) {
		t.Errorf("GetLanguage = %+v", langs)
	}
	if english := must(sc.GetLanguageById(ctx, 1)).Answer; english.Name != "English" || english.NameLower != "english" {
		t.Errorf("GetLanguageById(1) = %+v", english)
	}
	if flags := must(sc.GetIndexerFlag(ctx)).Answer; !slices.ContainsFunc(flags, func(f sonarr.IndexerFlagResource) bool { return f.Name == "Freeleech" }) {
		t.Errorf("GetIndexerFlag = %+v", flags)
	}

	// the UI's strings, in the language the UI is set to
	loc := must(sc.GetLocalization(ctx)).Answer
	if len(loc.Strings) < 100 || loc.Strings["Series"] != "Series" {
		t.Errorf("GetLocalization = %d strings", len(loc.Strings))
	}
	if byID := must(sc.GetLocalizationById(ctx, 1)).Answer; len(byID.Strings) != len(loc.Strings) {
		t.Errorf("GetLocalizationById = %d strings, want %d", len(byID.Strings), len(loc.Strings))
	}
	if lang := must(sc.GetLocalizationLanguage(ctx)).Answer; lang.Identifier != "en" {
		t.Errorf("GetLocalizationLanguage = %+v", lang)
	}
}

// The posters Sonarr downloads for a series are served from its own cache.
func TestMediaCover(t *testing.T) {
	ctx := skipUnlessUp(t)

	ff := seriesIDs[firefly.Title]
	res := must(sc.GetMediaCoverBySeriesIdByFilename(ctx, ff, "poster.jpg"))
	defer func() { _ = res.HttpResponse.Body.Close() }()
	img, err := io.ReadAll(res.HttpResponse.Body)
	// a JPEG: the real poster when recording, the proxy's stand-in on replay
	if err != nil || !bytes.HasPrefix(img, []byte{0xFF, 0xD8}) || res.HttpResponse.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("GetMediaCoverBySeriesIdByFilename(poster.jpg) = %d bytes of %s, %v", len(img), res.HttpResponse.Header.Get("Content-Type"), err)
	}
	if _, err := sc.GetMediaCoverBySeriesIdByFilename(ctx, ff, "clearlogo-sdk.jpg"); !isStatus(err, http.StatusNotFound) {
		t.Errorf("a cover Sonarr does not have = %v, want a 404", err)
	}
}

// A command has a method of its own, which names it and carries its fields, and a form of that method that waits for it to end. Chernobyl is one
// season with nothing in the indexer's catalogue, and renaming is off, so its searches and its rename are short and change nothing.
func TestCommands(t *testing.T) {
	ctx := skipUnlessUp(t)

	id := seriesIDs[chernobyl.Title]
	res := must(sc.RescanSeries(ctx, sonarr.RescanSeriesCommand{SeriesId: id}))
	status(t, res.HttpResponse, http.StatusCreated)
	// the command Sonarr queued has the series it was given
	if got := res.Body["seriesId"]; fmt.Sprint(got) != fmt.Sprint(id) {
		t.Errorf("RescanSeries queued for series %v, want %d: %v", got, id, res.Body)
	}

	cmd := must(sc.WaitForCommand(ctx, res.Id, commandWait)).Answer
	if cmd.Name != "RescanSeries" || cmd.Status != sonarr.CommandStatusCompleted || cmd.Trigger != sonarr.CommandTriggerManual || cmd.Ended == "" {
		t.Errorf("WaitForCommand = %+v", cmd)
	}
	if !slices.ContainsFunc(must(sc.GetCommand(ctx)).Answer, func(c sonarr.CommandResource) bool { return c.Id == cmd.Id }) {
		t.Error("GetCommand does not list the command")
	}
	// only a command still waiting to run can be cancelled: one that has run
	// is a conflict
	if _, err := sc.DeleteCommandById(ctx, cmd.Id); !isStatus(err, http.StatusConflict) {
		t.Errorf("cancelling a finished command = %v, want a 409", err)
	}

	// each of the rest ends completed, read back with the fields it was sent: a field Sonarr does not know is not among them
	completed := func(done sonarr.GetCommandByIdResponse, name string, fields map[string]any) {
		t.Helper()

		if done.Name != name || done.Status != sonarr.CommandStatusCompleted {
			t.Errorf("%s = %+v", name, done.Answer)
		}
		for field, want := range fields {
			if got := done.Body[field]; fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%s read back with %s %v, want %v", name, field, got, want)
			}
		}
	}
	e1 := episode(ctx, t, id, 1)
	files := must(sc.GetEpisodeFile(ctx, sonarr.GetEpisodeFileOptions{SeriesId: id})).Answer
	if len(files) == 0 {
		t.Fatal("the fixtures are incomplete: Chernobyl has no files")
	}
	file := files[0]
	// a refresh reads its one series into a list, and answers that
	completed(must(sc.RefreshSeriesAndWait(ctx, sonarr.RefreshSeriesCommand{SeriesId: id}, commandWait)), "RefreshSeries", map[string]any{"seriesIds": []sonarr.SeriesID{id}})
	completed(must(sc.SeriesSearchAndWait(ctx, sonarr.SeriesSearchCommand{SeriesId: id}, commandWait)), "SeriesSearch", map[string]any{"seriesId": id})
	completed(must(sc.SeasonSearchAndWait(ctx, sonarr.SeasonSearchCommand{SeriesId: id, SeasonNumber: new(1)}, commandWait)), "SeasonSearch", map[string]any{"seriesId": id, "seasonNumber": 1})
	completed(must(sc.EpisodeSearchAndWait(ctx, sonarr.EpisodeSearchCommand{EpisodeIds: []sonarr.EpisodeID{e1.Id}}, commandWait)), "EpisodeSearch", map[string]any{"episodeIds": []sonarr.EpisodeID{e1.Id}})
	completed(must(sc.MissingEpisodeSearchAndWait(ctx, sonarr.MissingEpisodeSearchCommand{SeriesId: id, Monitored: new(true)}, commandWait)), "MissingEpisodeSearch", map[string]any{"seriesId": id, "monitored": true})
	completed(must(sc.CutoffUnmetEpisodeSearchAndWait(ctx, sonarr.CutoffUnmetEpisodeSearchCommand{SeriesId: id, Monitored: new(true)}, commandWait)), "CutoffUnmetEpisodeSearch", map[string]any{"seriesId": id, "monitored": true})
	completed(must(sc.RenameFilesAndWait(ctx, sonarr.RenameFilesCommand{SeriesId: id, Files: []sonarr.EpisodeFileID{file.Id}}, commandWait)), "RenameFiles", map[string]any{"seriesId": id, "files": []sonarr.EpisodeFileID{file.Id}})
	if after := must(sc.GetEpisodeFileById(ctx, file.Id)).Answer; after.Path != file.Path {
		t.Errorf("RenameFiles with renaming off moved %s to %s", file.Path, after.Path)
	}
	if queue := must(sc.GetQueueComplete(ctx, sonarr.GetQueueOptions{SeriesIds: []sonarr.SeriesID{id}})).Items; len(queue) != 0 {
		t.Errorf("the searches for Chernobyl queued %d downloads, want none", len(queue))
	}

	// a command with no method of its own is posted by name, and an unknown one is a server error, not a bad request:
	// CommandController.StartCommand looks the name up with Single, which throws when nothing matches
	if health := runCommand(ctx, t, "CheckHealth"); health.Name != "CheckHealth" || health.Status != sonarr.CommandStatusCompleted {
		t.Errorf("CheckHealth = %+v", health)
	}
	if _, err := queueCommand(ctx, "SdkNoSuchCommand"); !isStatus(err, http.StatusInternalServerError) {
		t.Errorf("an unknown command = %v, want Sonarr's 500", err)
	}
}

// The calendar takes its tags as one comma-joined string, which the client holds as a list of tag ids: tags no series has leave the calendar
// empty, and Firefly's tag shows Firefly's episodes and no other's.
func TestCalendarByTag(t *testing.T) {
	ctx := skipUnlessUp(t)

	ff := seriesIDs[firefly.Title]
	other := newTag(ctx, t, "sdk-calendar-other")
	tag := newTag(ctx, t, "sdk-calendar")
	when := sonarr.GetCalendarOptions{Start: "2002-09-01", End: "2003-01-31", Unmonitored: new(true)}

	all := must(sc.GetCalendar(ctx, when)).Answer
	when.Tags = []sonarr.TagID{tag, other}
	if none := must(sc.GetCalendar(ctx, when)).Answer; len(all) == 0 || len(none) != 0 {
		t.Fatalf("the calendar holds %d episodes, and %d for two tags no series has", len(all), len(none))
	}

	tagSeries(ctx, t, ff, tag)
	tagged := must(sc.GetCalendar(ctx, when)).Answer
	if len(tagged) == 0 {
		t.Fatalf("the calendar for Firefly's tag is empty, of %d", len(all))
	}
	for i := range tagged {
		if tagged[i].SeriesId != ff {
			t.Errorf("the calendar for Firefly's tag holds an episode of series %d", tagged[i].SeriesId)
		}
	}
}
