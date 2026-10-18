//go:build acceptance

package sonarr_test

// The read sweep: every GET operation in the definitions, called against the
// running Sonarr with arguments resolved from the fixtures, and its answer
// decoded (or its file read). The bespoke tests prove the shapes the tools
// rely on field by field; the sweep proves the rest of the read surface
// answers in its documented status with something in it, that nothing the
// server sends is lost on the way into its model, and that it keeps
// doing so as the definitions change: a GET the importer adds is swept on the
// next run with nothing to write, and one that fails must be classified here.
//
// The sweep itself is pandorest's (github.com/katbyte/pandorest/sweep); what
// is here is this suite's fixtures and its cases. Every operation either
// answers with something, or has a case that says why not. A case whose
// operation starts answering fails the sweep, so a stale case is noticed and
// removed, the way a stale importer workaround is.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/pandorest/sweep"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// TestReadSweep calls every GET against the fixtures, with one of each thing
// Sonarr lists by id made for it.
func TestReadSweep(t *testing.T) {
	ctx := skipUnlessUp(t)

	ff := seriesIDs[firefly.Title]
	e1, e6, e7 := episode(ctx, t, ff, 1), episode(ctx, t, ff, 6), episode(ctx, t, ff, 7)
	tasks := must(sc.GetSystemTask(ctx)).Answer
	logs := must(sc.GetLogFile(ctx)).Answer
	metadata := must(sc.GetMetadata(ctx)).Answer
	if len(tasks) == 0 || len(logs) == 0 || len(metadata) == 0 || e1.EpisodeFileId == 0 {
		t.Fatalf("the fixtures are incomplete: %d tasks, %d log files, %d metadata consumers, S01E01's file %d",
			len(tasks), len(logs), len(metadata), e1.EpisodeFileId)
	}
	command := must(sc.RefreshMonitoredDownloadsAndWait(ctx, sonarr.RefreshMonitoredDownloadsCommand{}, commandWait))
	// Sonarr runs its health checks on a schedule of its own, so the list is
	// what the last run left: one is asked for now, so that what is swept is
	// what Sonarr makes of itself at this moment
	runCommand(ctx, t, "CheckHealth")
	made := sweepResources(ctx, t)
	// a series with a history: the downloads tests, which run first, leave
	// the only history a seeded Sonarr has
	recent := must(sc.GetHistory(ctx, sonarr.GetHistoryOptions{PageSize: new(1)})).Answer.Records
	if len(recent) == 0 {
		t.Fatal("the fixtures are incomplete: Sonarr has no history; the downloads tests make it, so run the whole suite")
	}

	id := func(n any) string { return fmt.Sprint(n) }
	// a path parameter is resolved by the segments before it and its name,
	// the most of them first, then by its name alone
	fixtures := map[string]string{
		"autotagging/id":         id(made.autoTagging),
		"calendar/id":            id(e1.Id),
		"command/id":             id(command.Id),
		"customfilter/id":        id(made.customFilter),
		"customformat/id":        id(made.customFormat),
		"cutoff/id":              id(e6.Id),
		"delayprofile/id":        "1", // the default profile, which cannot be deleted
		"detail/id":              id(made.tag),
		"downloadclient/id":      id(downloadClient),
		"episode/id":             id(e1.Id),
		"episodefile/id":         id(e1.EpisodeFileId),
		"file/filename":          logs[0].Filename,
		"importlist/id":          id(made.importList),
		"importlistexclusion/id": id(made.exclusion),
		"indexer/id":             id(indexerID),
		"language/id":            "1", // English
		"languageprofile/id":     "1", // the one fixed profile
		"localization/id":        "1", // any id answers the dictionary
		"mediacover/seriesId":    id(ff),
		"filename":               "poster.jpg",
		"metadata/id":            id(metadata[0].Id),
		"missing/id":             id(e7.Id),
		"notification/id":        id(made.notification),
		"qualitydefinition/id":   "1",
		"qualityprofile/id":      id(profileID),
		"releaseprofile/id":      id(made.releaseProfile),
		"remotepathmapping/id":   id(made.remotePathMapping),
		"rootfolder/id":          id(rootFolderID),
		"series/id":              id(ff),
		"tag/id":                 id(made.tag),
		"task/id":                id(tasks[0].Id),
		"update/filename":        logs[0].Filename,
		"host/id":                "1", // each settings section is one resource, id 1
		"mediamanagement/id":     "1",
		"naming/id":              "1",
		"ui/id":                  "1",
		// the settings sections that share their last segment with a list of
		// providers (/config/indexer/{id} beside /indexer/{id}) are one
		// resource each too, id 1, whatever the provider's id is
		"config/downloadclient/id": "1",
		"config/importlist/id":     "1",
		"config/indexer/id":        "1",
	}

	naming := must(sc.GetConfigNaming(ctx)).Answer
	cases := map[string]sweep.Case{
		// the examples are the formats given applied to a sample episode, and
		// Sonarr throws on a format left out
		"GetConfigNamingExamples": {Options: map[string]any{
			"RenameEpisodes": true, "StandardEpisodeFormat": naming.StandardEpisodeFormat, "DailyEpisodeFormat": naming.DailyEpisodeFormat,
			"AnimeEpisodeFormat": naming.AnimeEpisodeFormat, "SeriesFolderFormat": naming.SeriesFolderFormat,
			"SeasonFolderFormat": naming.SeasonFolderFormat, "SpecialsFolderFormat": naming.SpecialsFolderFormat,
			"MultiEpisodeStyle": naming.MultiEpisodeStyle,
		}},
		// episodes and files are listed for a series or by id, and a list of
		// neither is refused
		// the calendar is the days around today unless told, and nothing in
		// the fixtures airs now: Firefly's first run aired late in 2002
		"GetCalendar":             {Options: map[string]any{"Start": "2002-09-01T00:00:00Z", "End": "2002-12-31T00:00:00Z", "Unmonitored": true}},
		"GetEpisode":              {Options: map[string]any{"SeriesId": ff}},
		"GetEpisodeFile":          {Options: map[string]any{"SeriesId": ff}},
		"GetFileSystem":           {Options: map[string]any{"Path": "/tv/", "IncludeFiles": true}},
		"GetFileSystemMediaFiles": {Options: map[string]any{"Path": "/tv/" + firefly.Folder}},
		"GetFileSystemType":       {Options: map[string]any{"Path": "/tv"}},
		"GetHistorySeries":        {Options: map[string]any{"SeriesId": recent[0].SeriesId}},
		"GetHistorySince":         {Options: map[string]any{"Date": "2000-01-01T00:00:00Z"}},
		"GetManualImport":         {Options: map[string]any{"SeriesId": ff}},
		"GetParse":                {Options: map[string]any{"Title": "Firefly.S01E07.Safe.720p.HDTV.x264-SDK"}},
		// without an episode or a season, the release list is every indexer's
		// RSS feed; an episode is the interactive search the tools use
		"GetRelease": {Options: map[string]any{"EpisodeId": e7.Id}},
		"GetRename": {
			Options: map[string]any{"SeriesId": ff},
			Empty:   "Sonarr's rename preview is empty while renaming is off, which is how it is seeded; TestConfigNaming turns it on and reads Chernobyl's",
		},
		"GetSeriesLookup": {Options: map[string]any{"Term": "tvdb:" + id(firefly.TvdbID)}},
		"GetLogFileUpdateByFilename": {
			Status: http.StatusNotFound,
			Why:    "a container is updated by replacing its image, so Sonarr never runs its updater and has no update log to read",
		},
		"GetHealth":        {Empty: "a seeded Sonarr has nothing wrong with it, and its checks have just run; the tool suite's TestServerHealth and TestAuditHealth read the list with the checks its own setup trips"},
		"GetLogFileUpdate": {Empty: "a container is updated by replacing its image, so Sonarr never runs its updater and has no update log to list"},
		"GetSystemBackup":  {Empty: "there is no backup until one is made; TestBackups makes one and reads the list with it in"},
		"GetQueueDetails":  {Empty: "nothing is downloading when the sweep runs; TestDownloadImport reads the queue with a download in it"},
		"GetQueueStatus":   {Empty: "nothing is downloading when the sweep runs, so every count is zero; TestDownloadImport reads it with a download in the queue"},
	}

	svc, err := loadDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	// Strict: a field Sonarr sends that its document does not declare is a
	// failure, not a value the client drops without a word. A tool that reads
	// a series and writes it back would otherwise lose it on the way.
	sweep.Sweep{Definitions: svc, Client: sc, StatusCode: client.StatusCode, Fixtures: sweep.Fixtures{Path: fixtures}, Cases: cases, Strict: true}.Run(t)
}

// sweepMade is what the sweep makes to read back by id.
type sweepMade struct {
	tag                                                                                                             sonarr.TagID
	customFormat, customFilter, autoTagging, releaseProfile, remotePathMapping, exclusion, importList, notification int
}

// sweepResources makes one of each resource Sonarr has none of by default,
// each removed when the test ends.
func sweepResources(ctx context.Context, t *testing.T) sweepMade {
	t.Helper()

	var m sweepMade
	cleanup := func(del func(context.Context) error) {
		t.Cleanup(func() { _ = del(context.WithoutCancel(ctx)) })
	}
	m.tag = newTag(ctx, t, "sdk-sweep")

	x265 := formatSpec(t, must(sc.GetCustomFormatSchema(ctx)).Answer, "ReleaseTitleSpecification", "x265", `\bx265\b`)
	m.customFormat = must(sc.PostCustomFormat(ctx, sonarr.CustomFormatResource{
		Name: "SDK Sweep", IncludeCustomFormatWhenRenaming: new(false), Specifications: []sonarr.CustomFormatSpecificationSchema{x265},
	})).Answer.Id
	cleanup(func(ctx context.Context) error { _, err := sc.DeleteCustomFormatById(ctx, m.customFormat); return err })

	m.customFilter = must(sc.PostCustomFilter(ctx, sonarr.CustomFilterResource{
		Type: "series", Label: "SDK Sweep", Filters: []map[string]any{{"key": "monitored", "value": []any{true}, "type": "equal"}},
	})).Answer.Id
	cleanup(func(ctx context.Context) error { _, err := sc.DeleteCustomFilterById(ctx, m.customFilter); return err })

	genre := tagSpec(t, must(sc.GetAutoTaggingSchema(ctx)).Answer, "GenreSpecification", "Anime", []string{"Anime"})
	m.autoTagging = must(sc.PostAutoTagging(ctx, sonarr.AutoTaggingResource{
		Name: "SDK Sweep", RemoveTagsAutomatically: new(false), Tags: []sonarr.TagID{m.tag}, Specifications: []sonarr.AutoTaggingSpecificationSchema{genre},
	})).Answer.Id
	cleanup(func(ctx context.Context) error { _, err := sc.DeleteAutoTaggingById(ctx, m.autoTagging); return err })

	m.releaseProfile = must(sc.PostReleaseProfile(ctx, sonarr.ReleaseProfileResource{
		Name: "SDK Sweep", Enabled: new(true), Required: []string{"x265"}, Tags: []sonarr.TagID{m.tag},
	})).Answer.Id
	cleanup(func(ctx context.Context) error {
		_, err := sc.DeleteReleaseProfileById(ctx, m.releaseProfile)
		return err
	})

	m.remotePathMapping = must(sc.PostRemotePathMapping(ctx, sonarr.RemotePathMappingResource{
		Host: "sdk-sweep", RemotePath: "/remote/", LocalPath: "/downloads/",
	})).Answer.Id
	cleanup(func(ctx context.Context) error {
		_, err := sc.DeleteRemotePathMappingById(ctx, m.remotePathMapping)
		return err
	})

	m.exclusion = must(sc.PostImportListExclusion(ctx, sonarr.ImportListExclusionResource{TvdbId: 1, Title: "SDK Sweep"})).Answer.Id
	cleanup(func(ctx context.Context) error {
		_, err := sc.DeleteImportListExclusionById(ctx, m.exclusion)
		return err
	})

	m.importList = newImportList(ctx, t, "SDK Sweep").Id

	// a notification with no events is not tested when it is saved, so it
	// can point anywhere
	schemas := must(sc.GetNotificationSchema(ctx)).Answer
	i := slices.IndexFunc(schemas, func(s sonarr.NotificationResource) bool { return s.Implementation == "Webhook" })
	if i < 0 {
		t.Fatal("sonarr has no Webhook notification schema")
	}
	n := schemas[i]
	n.Name, n.Fields = "SDK Sweep", fields(n.Fields, map[string]any{"url": "http://localhost:9/sweep", "method": 1})
	m.notification = must(sc.PostNotification(ctx, n)).Answer.Id
	cleanup(func(ctx context.Context) error { _, err := sc.DeleteNotificationById(ctx, m.notification); return err })

	return m
}
