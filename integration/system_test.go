//go:build integration

package integration

// Sonarr about itself: its status, scheduled tasks, backups, logs, updates,
// health and disks, the file system as it sees it, the languages and
// translations it offers, the commands it runs, the posters it keeps, and
// the unversioned endpoints outside /api/v3.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

//nolint:paralleltest // the tests share one Sonarr
func TestSystemStatus(t *testing.T) {
	ctx := skipUnlessUp(t)

	st := must(sc.GetSystemStatus(ctx)).Model
	if !strings.HasPrefix(st.Version, "4.") || st.AppName != "Sonarr" || st.OsName == "" || st.StartTime == "" || !boolValue(st.IsDocker) {
		t.Errorf("GetSystemStatus = %+v", st)
	}

	// outside /api/v3: the API versions, and the ping a load balancer uses
	var api struct {
		Current    string   `json:"current"`
		Deprecated []string `json:"deprecated"`
	}
	if err := unmarshal(must(sc.GetApi(ctx)).Model, &api); err != nil || api.Current != "v3" {
		t.Errorf("GetApi = %+v, %v", api, err)
	}
	if ping := must(sc.GetPing(ctx)).Model; ping.Status != "OK" {
		t.Errorf("GetPing = %+v", ping)
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestSystemTasks(t *testing.T) {
	ctx := skipUnlessUp(t)

	tasks := must(sc.GetSystemTask(ctx)).Model
	i := slices.IndexFunc(tasks, func(x sonarr.TaskResource) bool { return x.TaskName == "RefreshMonitoredDownloads" })
	if i < 0 || tasks[i].Interval != 1 || tasks[i].NextExecution == "" {
		t.Fatalf("GetSystemTask = %+v, want the download check every minute", tasks)
	}
	if got := must(sc.GetSystemTaskById(ctx, tasks[i].Id)).Model; got.TaskName != tasks[i].TaskName {
		t.Errorf("GetSystemTaskById = %+v", got)
	}
}

// A backup is made by the Backup command, listed, and deleted.
//
//nolint:paralleltest // the tests share one Sonarr
func TestBackups(t *testing.T) {
	ctx := skipUnlessUp(t)

	before := must(sc.GetSystemBackup(ctx)).Model
	runCommand(ctx, t, "Backup", nil)
	after := must(sc.GetSystemBackup(ctx)).Model
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
	if slices.ContainsFunc(must(sc.GetSystemBackup(ctx)).Model, func(x sonarr.BackupResource) bool { return x.Id == b.Id }) {
		t.Error("the deleted backup is still listed")
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestLogs(t *testing.T) {
	ctx := skipUnlessUp(t)

	page := must(sc.GetLog(ctx, sonarr.GetLogOperationOptions{PageSize: 5, SortKey: "time", SortDirection: sonarr.SortDirectionDescending})).Model
	if len(page.Records) != 5 || page.TotalRecords < 5 || page.Records[0].Message == "" || page.Records[0].Level == "" {
		t.Errorf("GetLog, five newest = %+v", page)
	}

	files := must(sc.GetLogFile(ctx)).Model
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
	if updates := must(sc.GetLogFileUpdate(ctx)).Model; len(updates) != 0 {
		t.Logf("GetLogFileUpdate lists %d files", len(updates))
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestUpdatesHealthAndDisks(t *testing.T) {
	ctx := skipUnlessUp(t)

	// the recent releases, from Sonarr's update service (through the proxy)
	for _, u := range must(sc.GetUpdate(ctx)).Model {
		if u.Version == "" || u.ReleaseDate == "" {
			t.Errorf("an update = %+v", u)
		}
	}
	for _, h := range must(sc.GetHealth(ctx)).Model {
		if h.Source == "" || h.Message == "" {
			t.Errorf("a health check = %+v", h)
		}
	}
	// the disks the root folders are on, which in a container may be the
	// root file system alone: Sonarr leaves out mounts of the kinds it does
	// not count as disks, which is how a VM shares a host folder
	disks := must(sc.GetDiskSpace(ctx)).Model
	if len(disks) == 0 || slices.ContainsFunc(disks, func(d sonarr.DiskSpaceResource) bool { return d.Path == "" || d.TotalSpace < d.FreeSpace }) {
		t.Errorf("GetDiskSpace = %+v", disks)
	}
}

// The file system as Sonarr sees it, which is what its folder pickers show.
//
//nolint:paralleltest // the tests share one Sonarr
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
	if err := unmarshal(must(sc.GetFileSystem(ctx, sonarr.GetFileSystemOperationOptions{Path: "/tv/"})).Model, &listing); err != nil ||
		!slices.ContainsFunc(listing.Directories, func(d entry) bool { return d.Name == firefly.Folder && d.Type == "folder" }) {
		t.Errorf("GetFileSystem(/tv/) = %+v, %v", listing, err)
	}

	var media []entry
	if err := unmarshal(must(sc.GetFileSystemMediaFiles(ctx, sonarr.GetFileSystemMediaFilesOperationOptions{Path: "/tv/" + firefly.Folder})).Model, &media); err != nil ||
		len(media) < firefly.Files || !strings.HasSuffix(media[0].Name, ".mkv") {
		t.Fatalf("GetFileSystemMediaFiles = %+v, %v", media, err)
	}

	// a file is a file; anything else is answered as a folder, so the
	// picker gives nothing away about what is not there
	for path, want := range map[string]string{media[0].Path: "file", "/tv": "folder", "/nowhere": "folder"} {
		var kind struct {
			Type string `json:"type"`
		}
		if err := unmarshal(must(sc.GetFileSystemType(ctx, sonarr.GetFileSystemTypeOperationOptions{Path: path})).Model, &kind); err != nil || kind.Type != want {
			t.Errorf("GetFileSystemType(%s) = %+v, %v, want %s", path, kind, err, want)
		}
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestLanguagesAndLocalization(t *testing.T) {
	ctx := skipUnlessUp(t)

	langs := must(sc.GetLanguage(ctx)).Model
	if !slices.ContainsFunc(langs, func(l sonarr.LanguageResource) bool { return l.Id == 1 && l.Name == "English" }) {
		t.Errorf("GetLanguage = %+v", langs)
	}
	if english := must(sc.GetLanguageById(ctx, 1)).Model; english.Name != "English" || english.NameLower != "english" {
		t.Errorf("GetLanguageById(1) = %+v", english)
	}
	if flags := must(sc.GetIndexerFlag(ctx)).Model; !slices.ContainsFunc(flags, func(f sonarr.IndexerFlagResource) bool { return f.Name == "Freeleech" }) {
		t.Errorf("GetIndexerFlag = %+v", flags)
	}

	// the UI's strings, in the language the UI is set to
	loc := must(sc.GetLocalization(ctx)).Model
	if len(loc.Strings) < 100 || loc.Strings["Series"] != "Series" {
		t.Errorf("GetLocalization = %d strings", len(loc.Strings))
	}
	if byID := must(sc.GetLocalizationById(ctx, 1)).Model; len(byID.Strings) != len(loc.Strings) {
		t.Errorf("GetLocalizationById = %d strings, want %d", len(byID.Strings), len(loc.Strings))
	}
	if lang := must(sc.GetLocalizationLanguage(ctx)).Model; lang.Identifier != "en" {
		t.Errorf("GetLocalizationLanguage = %+v", lang)
	}
}

// The posters Sonarr downloads for a series are served from its own cache.
//
//nolint:paralleltest // the tests share one Sonarr
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

// A command carries its own fields at the top level of the body, which the
// generated command model cannot hold; the SDK posts them raw.
//
//nolint:paralleltest // the tests share one Sonarr
func TestCommands(t *testing.T) {
	ctx := skipUnlessUp(t)

	id := seriesIDs[severance.Title]
	res := must(sc.PostCommand(ctx, must(jsonRaw(map[string]any{"name": "RescanSeries", "seriesId": id}))))
	status(t, res.HttpResponse, http.StatusCreated)
	// the command Sonarr queued has the series it was given
	var echoed struct {
		Body struct {
			SeriesID int `json:"seriesId"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(bodyOf(t, res.HttpResponse)), &echoed); err != nil || echoed.Body.SeriesID != id {
		t.Errorf("PostCommand(RescanSeries) queued %+v, %v; want series %d", echoed, err, id)
	}

	cmd := res.Model
	if !poll(time.Minute, func() bool {
		cmd = must(sc.GetCommandById(ctx, cmd.Id)).Model
		return cmd.Status == sonarr.CommandStatusCompleted
	}) {
		t.Fatalf("RescanSeries never completed: %+v", cmd)
	}
	if cmd.Name != "RescanSeries" || cmd.Trigger != sonarr.CommandTriggerManual || cmd.Ended == "" {
		t.Errorf("GetCommandById = %+v", cmd)
	}
	if !slices.ContainsFunc(must(sc.GetCommand(ctx)).Model, func(c sonarr.CommandResource) bool { return c.Id == cmd.Id }) {
		t.Error("GetCommand does not list the command")
	}
	// only a command still waiting to run can be cancelled: one that has run
	// is a conflict
	if _, err := sc.DeleteCommandById(ctx, cmd.Id); !isStatus(err, http.StatusConflict) {
		t.Errorf("cancelling a finished command = %v, want a 409", err)
	}
	// an unknown command is a server error, not a bad request:
	// CommandController.StartCommand looks the name up with Single, which
	// throws when nothing matches
	if _, err := queueCommand(ctx, "SdkNoSuchCommand", nil); !isStatus(err, http.StatusInternalServerError) {
		t.Errorf("an unknown command = %v, want Sonarr's 500", err)
	}
}
