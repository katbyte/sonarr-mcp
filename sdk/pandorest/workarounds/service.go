// Package workarounds is what is sonarr-mcp's own about generating its
// client: the one service pandorest generates here, and a named fix for each
// thing Sonarr's API document gets wrong. The generator itself is
// github.com/katbyte/pandorest.
//
// Every workaround patches the loaded document before it is read, and every
// one first checks that the bug it fixes is still there: when the document no
// longer has the problem the workaround fails the import instead of silently
// doing nothing, so dead workarounds get deleted rather than accumulating.
// Only the shape of the document belongs here: what an operation takes and
// answers. Behaviour the document cannot express (a command that queues
// rather than runs, a list that ignores a filter) stays in the tools, next to
// the live tests that found it.
package workarounds

import (
	"fmt"
	"time"

	"github.com/katbyte/pandorest/config"
	pandorest "github.com/katbyte/pandorest/importer/workarounds"
	"github.com/katbyte/pandorest/openapi"
)

// Service is the name the document, its definitions and every workaround
// carry.
const Service = "sonarr"

// Services is the one API generated here.
var Services = []config.Service{{
	Name:    Service,
	Package: "sonarr",
	Output:  "sdk/sonarr",
	// Swashbuckle leaves the document without operationIds, so the methods are named from the method and the path, with the prefix every path carries left out: GET /api/v3/series/{id} is GetSeriesById
	Naming:     config.PathNaming,
	PathPrefix: "/api/v3",
	Words:      sonarrWords,
	Auth:       "APIKey",
	// what is Sonarr's own about being talked to is sdk/sonarr/service.go, written by hand beside the generated files
	ClientService: "service",
	// New is worded for Sonarr: its credential is an API key, and its address may carry a URL base
	Credential: "API key",
	NewDoc:     "New returns a client for the Sonarr server at baseURL (scheme and host,\noptionally the URL base it is configured with) that authenticates with its\nAPI key. The options are the base client's: client.WithLog to trace what\nis sent, client.WithTransport to send it through a transport of the\ncaller's own.",
	// history, the queue, the blocklist, the wanted lists, the exclusions and the log page by number, and answer a PagingResource
	Paging:       &config.Paging{Start: "page", Limit: "pageSize", Items: "records", Total: "totalRecords", ByPage: true},
	WrittenWhole: sonarrSettings,
	// Sonarr writes a date and time as RFC 3339 in UTC ("2026-09-19T13:00:00Z"), with year one for never, which reads as no date; an episode's
	// air date is a day on its own ("2026-09-19")
	Dates: config.Dates{Send: time.RFC3339},
	Days:  config.Dates{Send: time.DateOnly},
	// the ids that travel between calls are told apart by the compiler; the rest stay plain numbers
	IDs:   sonarrIDs,
	Plain: sonarrPlainIDs,
	// season 0 is the specials, so a season search sends its season whenever one is set
	KeepNull: []string{"SeasonSearchCommand.seasonNumber"},
	// Swashbuckle lists text/plain beside every JSON response, which Sonarr never sends for them
	PreferJSON: true,
	// a test of every indexer, download client, import list or connection answers 400 with the same list of results when any of them fails
	ExpectSameShape: true,
}}

// sonarrSettings are Sonarr's settings sections, each one object read with a
// GET and saved with a PUT of the whole of it. Sonarr saves a section field
// by field, and a string left out arrives as null: the host settings'
// ConfigFileProvider.SaveConfigDictionary calls ToString on it and answers
// 500, and the others' ConfigService.SaveConfigDictionary skips it, so a
// setting cleared to "" is quietly kept instead.
var sonarrSettings = []string{
	"DownloadClientConfigResource",
	"HostConfigResource",
	"ImportListConfigResource",
	"IndexerConfigResource",
	"MediaManagementConfigResource",
	"NamingConfigResource",
	"UiConfigResource",
}

// sonarrWords are the path segments of Sonarr's API that run two or more
// words together, spelled the way its tags and models spell them.
var sonarrWords = map[string]string{
	"autotagging":         "AutoTagging",
	"customfilter":        "CustomFilter",
	"customformat":        "CustomFormat",
	"delayprofile":        "DelayProfile",
	"diskspace":           "DiskSpace",
	"downloadclient":      "DownloadClient",
	"episodefile":         "EpisodeFile",
	"filesystem":          "FileSystem",
	"importlist":          "ImportList",
	"importlistexclusion": "ImportListExclusion",
	"indexerflag":         "IndexerFlag",
	"languageprofile":     "LanguageProfile",
	"manualimport":        "ManualImport",
	"mediacover":          "MediaCover",
	"mediafiles":          "MediaFiles",
	"mediamanagement":     "MediaManagement",
	"qualitydefinition":   "QualityDefinition",
	"qualityprofile":      "QualityProfile",
	"releaseprofile":      "ReleaseProfile",
	"remotepathmapping":   "RemotePathMapping",
	"rootfolder":          "RootFolder",
	"seasonpass":          "SeasonPass",
	"testall":             "TestAll",
}

// All is every workaround, in the order they are applied.
var All = []pandorest.Workaround{
	// Swashbuckle types the id of every PUT path as text, where the GET and DELETE of the same path take a number
	pandorest.NumberedPathIDs(pandorest.About{Name: "sonarr-numbered-path-ids", Service: Service, Bug: "a PUT path types its {id} as text where the GET and DELETE of the same path type it as a number"}),
	sonarrUIRoutes,
	sonarrAnswerRecords,
	sonarrUndeclaredResponses,
	sonarrUndeclaredWriteResponses,
	sonarrCreatedAccepted,
	sonarrTestResults,
	sonarrTestAllResults,
	sonarrCommandBody{},
	// after the body is an open record: the commands the tools queue get a record and a method each
	pandorest.Commands(pandorest.About{Name: "sonarr-commands", Service: Service, Bug: "POST /api/v3/command takes every command through one open body, so nothing says what a command is called or which fields it reads"}, sonarrCommands),
	sonarrCommandResourceBody{},
	sonarrHTTPURIString{},
	sonarrAirDays,
	sonarrBulkUpdateLists,
}

// operation finds an operation a workaround targets.
func operation(spec *openapi.Spec, method, path string) (*openapi.Operation, error) {
	op := spec.Operation(method, path)
	if op == nil {
		return nil, fmt.Errorf("%s %s is not in the document", method, path)
	}

	return op, nil
}
