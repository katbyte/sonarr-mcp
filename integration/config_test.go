//go:build integration

package integration

// The settings: each section read, read again by its id, and written back.
// A section is one object Sonarr keeps whole, so the write-back is the
// round trip every settings change makes - read it, change a field, send the
// lot - and it has to leave everything else as it was.

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

//nolint:paralleltest // the tests share one Sonarr
func TestConfigNaming(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigNaming(ctx)).Model
	if cfg.StandardEpisodeFormat == "" || cfg.SeriesFolderFormat == "" || cfg.SeasonFolderFormat == "" {
		t.Fatalf("GetConfigNaming = %+v", cfg)
	}
	if byID := must(sc.GetConfigNamingById(ctx, cfg.Id)).Model; byID.StandardEpisodeFormat != cfg.StandardEpisodeFormat {
		t.Errorf("GetConfigNamingById = %+v", byID)
	}
	// the examples are the formats given applied to a sample episode: the
	// settings page previews a form not yet saved
	examples := must(sc.GetConfigNamingExamples(ctx, sonarr.GetConfigNamingExamplesOperationOptions{
		RenameEpisodes: new(true), StandardEpisodeFormat: cfg.StandardEpisodeFormat, DailyEpisodeFormat: cfg.DailyEpisodeFormat,
		AnimeEpisodeFormat: cfg.AnimeEpisodeFormat, SeriesFolderFormat: cfg.SeriesFolderFormat, SeasonFolderFormat: cfg.SeasonFolderFormat,
		SpecialsFolderFormat: cfg.SpecialsFolderFormat, MultiEpisodeStyle: cfg.MultiEpisodeStyle,
	})).Model
	var ex map[string]any
	if err := unmarshal(examples, &ex); err != nil || ex["singleEpisodeExample"] == nil || ex["seriesFolderExample"] == nil {
		t.Errorf("GetConfigNamingExamples = %s, %v", examples, err)
	}

	// with renaming off, Sonarr keeps the names files arrive with and its
	// rename preview is empty whatever they are; on, it lists Chernobyl's
	// scene-named files
	chernobylID := seriesIDs[chernobyl.Title]
	if plan := must(sc.GetRename(ctx, sonarr.GetRenameOperationOptions{SeriesId: chernobylID})).Model; boolValue(cfg.RenameEpisodes) || len(plan) != 0 {
		t.Errorf("with renaming %v, GetRename = %d renames, want none while it is off", boolValue(cfg.RenameEpisodes), len(plan))
	}
	on := cfg
	on.RenameEpisodes = new(true)
	res := must(sc.PutConfigNamingById(ctx, strconv.Itoa(cfg.Id), on))
	status(t, res.HttpResponse, http.StatusAccepted)
	t.Cleanup(func() { _, _ = sc.PutConfigNamingById(ctx, strconv.Itoa(cfg.Id), cfg) })
	if !boolValue(res.Model.RenameEpisodes) || res.Model.StandardEpisodeFormat != cfg.StandardEpisodeFormat {
		t.Errorf("PutConfigNamingById = %+v", res.Model)
	}
	plan := must(sc.GetRename(ctx, sonarr.GetRenameOperationOptions{SeriesId: chernobylID})).Model
	if len(plan) != chernobyl.Files || plan[0].EpisodeFileId == 0 || plan[0].ExistingPath == plan[0].NewPath {
		t.Errorf("GetRename with renaming on = %+v, want the %d scene-named files", plan, chernobyl.Files)
	}
	season := must(sc.GetRename(ctx, sonarr.GetRenameOperationOptions{SeriesId: chernobylID, SeasonNumber: 1})).Model
	if len(season) != len(plan) {
		t.Errorf("GetRename for season 1 = %d renames, want %d", len(season), len(plan))
	}

	status(t, must(sc.PutConfigNamingById(ctx, strconv.Itoa(cfg.Id), cfg)).HttpResponse, http.StatusAccepted)
	if got := must(sc.GetConfigNaming(ctx)).Model; boolValue(got.RenameEpisodes) != boolValue(cfg.RenameEpisodes) {
		t.Errorf("the naming settings were not put back: %+v", got)
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestConfigMediaManagement(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigMediaManagement(ctx)).Model
	if cfg.Id == 0 || cfg.FileDate == "" || cfg.RescanAfterRefresh == "" || cfg.MinimumFreeSpaceWhenImporting == 0 {
		t.Fatalf("GetConfigMediaManagement = %+v", cfg)
	}
	if byID := must(sc.GetConfigMediaManagementById(ctx, cfg.Id)).Model; byID.MinimumFreeSpaceWhenImporting != cfg.MinimumFreeSpaceWhenImporting {
		t.Errorf("GetConfigMediaManagementById = %+v", byID)
	}
	changed := cfg
	changed.MinimumFreeSpaceWhenImporting++
	res := must(sc.PutConfigMediaManagementById(ctx, strconv.Itoa(cfg.Id), changed))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Model.MinimumFreeSpaceWhenImporting != changed.MinimumFreeSpaceWhenImporting || res.Model.FileDate != cfg.FileDate {
		t.Errorf("PutConfigMediaManagementById = %+v", res.Model)
	}
	status(t, must(sc.PutConfigMediaManagementById(ctx, strconv.Itoa(cfg.Id), cfg)).HttpResponse, http.StatusAccepted)
	if got := must(sc.GetConfigMediaManagement(ctx)).Model; got.MinimumFreeSpaceWhenImporting != cfg.MinimumFreeSpaceWhenImporting || got.RecycleBin != cfg.RecycleBin {
		t.Errorf("the media management settings were not put back: %+v", got)
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestConfigUI(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigUi(ctx)).Model
	if cfg.Id == 0 || cfg.CalendarWeekColumnHeader == "" || cfg.ShortDateFormat == "" || cfg.UiLanguage == 0 {
		t.Fatalf("GetConfigUi = %+v", cfg)
	}
	if byID := must(sc.GetConfigUiById(ctx, cfg.Id)).Model; byID.ShortDateFormat != cfg.ShortDateFormat {
		t.Errorf("GetConfigUiById = %+v", byID)
	}
	changed := cfg
	changed.FirstDayOfWeek = 1 - cfg.FirstDayOfWeek
	res := must(sc.PutConfigUiById(ctx, strconv.Itoa(cfg.Id), changed))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Model.FirstDayOfWeek != changed.FirstDayOfWeek {
		t.Errorf("PutConfigUiById = %+v", res.Model)
	}
	status(t, must(sc.PutConfigUiById(ctx, strconv.Itoa(cfg.Id), cfg)).HttpResponse, http.StatusAccepted)
}

// The host settings hold the API key the suite runs on, the port and the
// authentication: the write-back sends them as read, and the key is checked
// to be the same afterwards (Sonarr never writes a key sent in the body).
//
//nolint:paralleltest // the tests share one Sonarr
func TestConfigHost(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigHost(ctx)).Model
	if cfg.Id == 0 || cfg.Port != 8989 || cfg.ApiKey == "" || cfg.InstanceName == "" || cfg.Branch == "" {
		t.Fatalf("GetConfigHost = %+v", cfg)
	}
	if byID := must(sc.GetConfigHostById(ctx, cfg.Id)).Model; byID.Port != cfg.Port || byID.ApiKey != cfg.ApiKey {
		t.Errorf("GetConfigHostById = %+v", byID)
	}
	// Sonarr 4.0.20 refuses to save the host settings while Allowed Hosts is
	// empty and authentication is not required - its own settings page
	// included - and a config.xml written before the setting existed leaves
	// it empty. The hosts the suite reaches Sonarr by (127.0.0.1) and Sonarr
	// reaches itself by (localhost) keep everything allowed; there is no
	// putting it back to empty, which Sonarr refuses, and no "*", which it
	// refuses too.
	if cfg.AllowedHosts == "" {
		cfg.AllowedHosts = "127.0.0.1,localhost"
	}
	changed := cfg
	changed.BackupRetention = cfg.BackupRetention - 1
	res := must(sc.PutConfigHostById(ctx, strconv.Itoa(cfg.Id), changed))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Model.BackupRetention != changed.BackupRetention {
		t.Errorf("PutConfigHostById = %+v", res.Model)
	}
	status(t, must(sc.PutConfigHostById(ctx, strconv.Itoa(cfg.Id), cfg)).HttpResponse, http.StatusAccepted)
	got := must(sc.GetConfigHost(ctx)).Model
	if got.ApiKey != cfg.ApiKey || got.Port != cfg.Port || got.UrlBase != cfg.UrlBase || got.BackupRetention != cfg.BackupRetention {
		t.Errorf("the host settings changed under a write-back: %+v", got)
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestConfigIndexer(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigIndexer(ctx)).Model
	if cfg.Id == 0 || cfg.RssSyncInterval == 0 || cfg.Retention < 0 {
		t.Fatalf("GetConfigIndexer = %+v", cfg)
	}
	if byID := must(sc.GetConfigIndexerById(ctx, cfg.Id)).Model; byID.RssSyncInterval != cfg.RssSyncInterval {
		t.Errorf("GetConfigIndexerById = %+v", byID)
	}
	changed := cfg
	changed.RssSyncInterval = cfg.RssSyncInterval + 5
	res := must(sc.PutConfigIndexerById(ctx, strconv.Itoa(cfg.Id), changed))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Model.RssSyncInterval != changed.RssSyncInterval {
		t.Errorf("PutConfigIndexerById = %+v", res.Model)
	}
	status(t, must(sc.PutConfigIndexerById(ctx, strconv.Itoa(cfg.Id), cfg)).HttpResponse, http.StatusAccepted)
}

//nolint:paralleltest // the tests share one Sonarr
func TestConfigDownloadClient(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigDownloadClient(ctx)).Model
	// the seed turned the re-search after a failure off
	if cfg.Id == 0 || cfg.DownloadClientWorkingFolders == "" || boolValue(cfg.AutoRedownloadFailed) {
		t.Fatalf("GetConfigDownloadClient = %+v", cfg)
	}
	if byID := must(sc.GetConfigDownloadClientById(ctx, cfg.Id)).Model; byID.DownloadClientWorkingFolders != cfg.DownloadClientWorkingFolders {
		t.Errorf("GetConfigDownloadClientById = %+v", byID)
	}
	res := must(sc.PutConfigDownloadClientById(ctx, strconv.Itoa(cfg.Id), cfg))
	status(t, res.HttpResponse, http.StatusAccepted)
	if boolValue(res.Model.AutoRedownloadFailed) || res.Model.DownloadClientWorkingFolders != cfg.DownloadClientWorkingFolders {
		t.Errorf("PutConfigDownloadClientById = %+v", res.Model)
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestConfigImportList(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigImportList(ctx)).Model
	if cfg.Id == 0 || cfg.ListSyncLevel == "" {
		t.Fatalf("GetConfigImportList = %+v", cfg)
	}
	if byID := must(sc.GetConfigImportListById(ctx, cfg.Id)).Model; byID.ListSyncLevel != cfg.ListSyncLevel {
		t.Errorf("GetConfigImportListById = %+v", byID)
	}
	res := must(sc.PutConfigImportListById(ctx, strconv.Itoa(cfg.Id), cfg))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Model.ListSyncLevel != cfg.ListSyncLevel {
		t.Errorf("PutConfigImportListById = %+v", res.Model)
	}
}

// boolValue reads an optional flag, false when unset.
func boolValue(b *bool) bool { return b != nil && *b }
