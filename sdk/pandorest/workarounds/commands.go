package workarounds

import (
	pandorest "github.com/katbyte/pandorest/importer/workarounds"
	"github.com/katbyte/pandorest/openapi"
)

// The shapes a command's fields take.
var (
	number = &openapi.Schema{Type: openapi.TypeInteger, Format: "int32"}
	// a number where 0 means something, so it is sent whenever it is set: season 0 is the specials
	counted = &openapi.Schema{Type: openapi.TypeInteger, Format: "int32", Nullable: true}
	numbers = &openapi.Schema{Type: openapi.TypeArray, Items: number}
	flag    = &openapi.Schema{Type: openapi.TypeBoolean}
	text    = &openapi.Schema{Type: openapi.TypeString}
	records = &openapi.Schema{Type: openapi.TypeArray, Items: &openapi.Schema{Type: openapi.TypeObject}}
)

// sonarrCommands are the commands the tools queue by name, each with the fields it reads from the body beside its name. Sonarr's document
// describes none of them: it has one open body for every command. A scheduled task run by the name Sonarr lists it under is queued the plain way.
var sonarrCommands = pandorest.CommandSet{
	Post:     "POST /api/v3/command",
	Get:      "GET /api/v3/command/{id}",
	Status:   "status",
	Finished: []string{"completed", "failed", "aborted", "cancelled", "orphaned"},
	Failed:   []string{"failed", "aborted", "cancelled", "orphaned"},
	Commands: map[string]map[string]*openapi.Schema{
		"Backup":                    {},
		"RefreshMonitoredDownloads": {},
		"RefreshSeries":             {"seriesId": number},
		"RescanSeries":              {"seriesId": number},
		"SeriesSearch":              {"seriesId": number},
		"SeasonSearch":              {"seriesId": number, "seasonNumber": counted},
		"EpisodeSearch":             {"episodeIds": numbers},
		// with no series, every series is searched
		"MissingEpisodeSearch":     {"seriesId": number, "monitored": flag},
		"CutoffUnmetEpisodeSearch": {"seriesId": number, "monitored": flag},
		// files are the episode files to rename, by id
		"RenameFiles": {"seriesId": number, "files": numbers},
		// files are what to import, each as the manual import list answered it with the series and episodes chosen
		"ManualImport": {"files": records, "importMode": text},
	},
}
