//go:build acceptance

package sonarr_test

// The settings: each section read, read again by its id, and written back.
// A section is one object Sonarr keeps whole, so the write-back is the
// round trip every settings change makes - read it, change a field, send the
// lot - and it has to leave everything else as it was.

import (
	"net/http"
	"testing"

	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

func TestConfigNaming(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigNaming(ctx)).Answer
	if cfg.StandardEpisodeFormat == "" || cfg.SeriesFolderFormat == "" || cfg.SeasonFolderFormat == "" {
		t.Fatalf("GetConfigNaming = %+v", cfg)
	}
	if byID := must(sc.GetConfigNamingById(ctx, cfg.Id)).Answer; byID.StandardEpisodeFormat != cfg.StandardEpisodeFormat {
		t.Errorf("GetConfigNamingById = %+v", byID)
	}
	// the examples are the formats given applied to a sample episode: the
	// settings page previews a form not yet saved
	examples := must(sc.GetConfigNamingExamples(ctx, sonarr.GetConfigNamingExamplesOptions{
		RenameEpisodes: new(true), StandardEpisodeFormat: cfg.StandardEpisodeFormat, DailyEpisodeFormat: cfg.DailyEpisodeFormat,
		AnimeEpisodeFormat: cfg.AnimeEpisodeFormat, SeriesFolderFormat: cfg.SeriesFolderFormat, SeasonFolderFormat: cfg.SeasonFolderFormat,
		SpecialsFolderFormat: cfg.SpecialsFolderFormat, MultiEpisodeStyle: new(cfg.MultiEpisodeStyle),
	})).Answer
	var ex map[string]any
	if err := unmarshal(examples, &ex); err != nil || ex["singleEpisodeExample"] == nil || ex["seriesFolderExample"] == nil {
		t.Errorf("GetConfigNamingExamples = %s, %v", examples, err)
	}

	// with renaming off, Sonarr keeps the names files arrive with and its
	// rename preview is empty whatever they are; on, it lists Chernobyl's
	// scene-named files
	chernobylID := seriesIDs[chernobyl.Title]
	if plan := must(sc.GetRename(ctx, sonarr.GetRenameOptions{SeriesId: chernobylID})).Answer; cfg.RenameEpisodes || len(plan) != 0 {
		t.Errorf("with renaming %v, GetRename = %d renames, want none while it is off", cfg.RenameEpisodes, len(plan))
	}
	on := cfg
	on.RenameEpisodes = true
	res := must(sc.PutConfigNamingById(ctx, cfg.Id, on))
	status(t, res.HttpResponse, http.StatusAccepted)
	t.Cleanup(func() { _, _ = sc.PutConfigNamingById(ctx, cfg.Id, cfg) })
	if !res.Answer.RenameEpisodes || res.Answer.StandardEpisodeFormat != cfg.StandardEpisodeFormat {
		t.Errorf("PutConfigNamingById = %+v", res.Answer)
	}
	plan := must(sc.GetRename(ctx, sonarr.GetRenameOptions{SeriesId: chernobylID})).Answer
	if len(plan) != chernobyl.Files || plan[0].EpisodeFileId == 0 || plan[0].ExistingPath == plan[0].NewPath {
		t.Errorf("GetRename with renaming on = %+v, want the %d scene-named files", plan, chernobyl.Files)
	}
	season := must(sc.GetRename(ctx, sonarr.GetRenameOptions{SeriesId: chernobylID, SeasonNumber: new(1)})).Answer
	if len(season) != len(plan) {
		t.Errorf("GetRename for season 1 = %d renames, want %d", len(season), len(plan))
	}

	status(t, must(sc.PutConfigNamingById(ctx, cfg.Id, cfg)).HttpResponse, http.StatusAccepted)
	if got := must(sc.GetConfigNaming(ctx)).Answer; got.RenameEpisodes != cfg.RenameEpisodes {
		t.Errorf("the naming settings were not put back: %+v", got)
	}
}

func TestConfigMediaManagement(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigMediaManagement(ctx)).Answer
	if cfg.Id == 0 || cfg.FileDate == "" || cfg.RescanAfterRefresh == "" || cfg.MinimumFreeSpaceWhenImporting == 0 {
		t.Fatalf("GetConfigMediaManagement = %+v", cfg)
	}
	if byID := must(sc.GetConfigMediaManagementById(ctx, cfg.Id)).Answer; byID.MinimumFreeSpaceWhenImporting != cfg.MinimumFreeSpaceWhenImporting {
		t.Errorf("GetConfigMediaManagementById = %+v", byID)
	}
	changed := cfg
	changed.MinimumFreeSpaceWhenImporting++
	res := must(sc.PutConfigMediaManagementById(ctx, cfg.Id, changed))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Answer.MinimumFreeSpaceWhenImporting != changed.MinimumFreeSpaceWhenImporting || res.Answer.FileDate != cfg.FileDate {
		t.Errorf("PutConfigMediaManagementById = %+v", res.Answer)
	}
	status(t, must(sc.PutConfigMediaManagementById(ctx, cfg.Id, cfg)).HttpResponse, http.StatusAccepted)
	if got := must(sc.GetConfigMediaManagement(ctx)).Answer; got.MinimumFreeSpaceWhenImporting != cfg.MinimumFreeSpaceWhenImporting || got.RecycleBin != cfg.RecycleBin {
		t.Errorf("the media management settings were not put back: %+v", got)
	}
}

func TestConfigUI(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigUi(ctx)).Answer
	if cfg.Id == 0 || cfg.CalendarWeekColumnHeader == "" || cfg.ShortDateFormat == "" || cfg.UiLanguage == 0 {
		t.Fatalf("GetConfigUi = %+v", cfg)
	}
	if byID := must(sc.GetConfigUiById(ctx, cfg.Id)).Answer; byID.ShortDateFormat != cfg.ShortDateFormat {
		t.Errorf("GetConfigUiById = %+v", byID)
	}
	changed := cfg
	changed.FirstDayOfWeek = 1 - cfg.FirstDayOfWeek
	res := must(sc.PutConfigUiById(ctx, cfg.Id, changed))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Answer.FirstDayOfWeek != changed.FirstDayOfWeek {
		t.Errorf("PutConfigUiById = %+v", res.Answer)
	}
	status(t, must(sc.PutConfigUiById(ctx, cfg.Id, cfg)).HttpResponse, http.StatusAccepted)
}

// The host settings hold the API key the suite runs on, the port and the
// authentication: the write-back sends them as read, and the key is checked
// to be the same afterwards (Sonarr never writes a key sent in the body).
func TestConfigHost(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigHost(ctx)).Answer
	if cfg.Id == 0 || cfg.Port != 8989 || cfg.ApiKey == "" || cfg.InstanceName == "" || cfg.Branch == "" {
		t.Fatalf("GetConfigHost = %+v", cfg)
	}
	if byID := must(sc.GetConfigHostById(ctx, cfg.Id)).Answer; byID.Port != cfg.Port || byID.ApiKey != cfg.ApiKey {
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
	res := must(sc.PutConfigHostById(ctx, cfg.Id, changed))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Answer.BackupRetention != changed.BackupRetention {
		t.Errorf("PutConfigHostById = %+v", res.Answer)
	}
	status(t, must(sc.PutConfigHostById(ctx, cfg.Id, cfg)).HttpResponse, http.StatusAccepted)
	got := must(sc.GetConfigHost(ctx)).Answer
	if got.ApiKey != cfg.ApiKey || got.Port != cfg.Port || got.UrlBase != cfg.UrlBase || got.BackupRetention != cfg.BackupRetention {
		t.Errorf("the host settings changed under a write-back: %+v", got)
	}
}

func TestConfigIndexer(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigIndexer(ctx)).Answer
	if cfg.Id == 0 || cfg.RssSyncInterval == 0 || cfg.Retention < 0 {
		t.Fatalf("GetConfigIndexer = %+v", cfg)
	}
	if byID := must(sc.GetConfigIndexerById(ctx, cfg.Id)).Answer; byID.RssSyncInterval != cfg.RssSyncInterval {
		t.Errorf("GetConfigIndexerById = %+v", byID)
	}
	changed := cfg
	changed.RssSyncInterval = cfg.RssSyncInterval + 5
	res := must(sc.PutConfigIndexerById(ctx, cfg.Id, changed))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Answer.RssSyncInterval != changed.RssSyncInterval {
		t.Errorf("PutConfigIndexerById = %+v", res.Answer)
	}
	status(t, must(sc.PutConfigIndexerById(ctx, cfg.Id, cfg)).HttpResponse, http.StatusAccepted)
}

func TestConfigDownloadClient(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigDownloadClient(ctx)).Answer
	// the seed turned the re-search after a failure off
	if cfg.Id == 0 || cfg.DownloadClientWorkingFolders == "" || cfg.AutoRedownloadFailed {
		t.Fatalf("GetConfigDownloadClient = %+v", cfg)
	}
	if byID := must(sc.GetConfigDownloadClientById(ctx, cfg.Id)).Answer; byID.DownloadClientWorkingFolders != cfg.DownloadClientWorkingFolders {
		t.Errorf("GetConfigDownloadClientById = %+v", byID)
	}
	res := must(sc.PutConfigDownloadClientById(ctx, cfg.Id, cfg))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Answer.AutoRedownloadFailed || res.Answer.DownloadClientWorkingFolders != cfg.DownloadClientWorkingFolders {
		t.Errorf("PutConfigDownloadClientById = %+v", res.Answer)
	}
}

func TestConfigImportList(t *testing.T) {
	ctx := skipUnlessUp(t)

	cfg := *must(sc.GetConfigImportList(ctx)).Answer
	if cfg.Id == 0 || cfg.ListSyncLevel == "" {
		t.Fatalf("GetConfigImportList = %+v", cfg)
	}
	if byID := must(sc.GetConfigImportListById(ctx, cfg.Id)).Answer; byID.ListSyncLevel != cfg.ListSyncLevel {
		t.Errorf("GetConfigImportListById = %+v", byID)
	}
	res := must(sc.PutConfigImportListById(ctx, cfg.Id, cfg))
	status(t, res.HttpResponse, http.StatusAccepted)
	if res.Answer.ListSyncLevel != cfg.ListSyncLevel {
		t.Errorf("PutConfigImportListById = %+v", res.Answer)
	}
}

// boolValue reads an optional flag, false when unset.
func boolValue(b *bool) bool { return b != nil && *b }
