package workarounds

import "github.com/katbyte/pandorest/config"

// sonarrIDs are the kinds of id that get a type of their own, so one passed where another belongs does not compile: the ones that travel between
// calls as bare numbers.
var sonarrIDs = []config.ID{
	// a release Sonarr has matched to a series names it as mappedSeriesId, and the season pass names each series by a bare id
	{Name: "SeriesID", Of: []string{"SeriesResource"}, Names: []string{"seriesId", "seriesIds", "mappedSeriesId"}, In: []string{"SeasonPassSeriesResource .id"}},
	// a release names the episodes Sonarr matched it to by a bare id
	{Name: "EpisodeID", Of: []string{"EpisodeResource"}, Names: []string{"episodeId", "episodeIds"}, In: []string{"ReleaseEpisodeResource .id"}},
	// the rename command names the files to rename by a bare list
	{Name: "EpisodeFileID", Of: []string{"EpisodeFileResource"}, Names: []string{"episodeFileId", "episodeFileIds"}, In: []string{"RenameFilesCommand .files"}},
	// a tag's details are read by the tag's own id, and the calendar takes its tags as one comma-joined string
	{Name: "TagID", Of: []string{"TagResource", "TagDetailsResource"}, Names: []string{"tags", "tagIds"}, Joined: []string{"tags"}},
	{Name: "QualityProfileID", Of: []string{"QualityProfileResource"}, Names: []string{"qualityProfileId"}},
	{Name: "CommandID", Of: []string{"CommandResource"}},
	// TheTVDB's id of a series; an episode's tvdbId is TheTVDB's id of that episode, a different thing under the same name
	{Name: "TvdbID", Base: "int", Names: []string{"tvdbId"}, Except: []string{"EpisodeResource .tvdbId"}},
}

// sonarrPlainIDs are the names that look like ids and are left as plain numbers or text on purpose: the settings records, which one layer of the
// tools handles alike, the ids other services gave a series, and the ones that are text already.
var sonarrPlainIDs = []string{
	"id", "ids",
	"autoTagIds", "delayProfileIds", "downloadClientId", "downloadClientIds", "importListIds", "indexerId", "indexerIds", "notificationIds", "restrictionIds",
	"languageProfileId",
	"imdbId", "tmdbId", "tvMazeId", "tvRageId",
	"downloadId", "guid",
}
