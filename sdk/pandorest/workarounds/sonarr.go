package workarounds

import (
	"errors"
	"fmt"
	"net/http"

	pandorest "github.com/katbyte/pandorest/importer/workarounds"
	"github.com/katbyte/pandorest/openapi"
)

// The Sonarr document is the one in the release's source tree,
// src/Sonarr.Api.V3/openapi.json, which Swashbuckle writes from the
// controllers. It describes the request side well and the answers poorly:
// every operation is documented as a 200, and an action returning object or
// IActionResult is documented as answering nothing. What follows was read off
// the controllers (Sonarr.Api.V3, Sonarr.Http) and confirmed by the
// SDK suite (acceptance/sonarr) against the same release.

const sonarr = Service

// sonarrUIRoutes takes out the routes that serve the web interface: the single page app, its static files, and the forms login.
var sonarrUIRoutes = pandorest.NotAPI(pandorest.About{
	Name: "sonarr-ui-routes", Service: sonarr,
	Bug: "the document includes the routes that serve the web interface (the page, its static files, the forms login), which answer HTML to a browser rather than anything an API client reads",
}, "/", "/{path}", "/content/{path}", "/login", "/logout")

// sonarrAnswerRecords adds the records the file system and a series' folder answer in, as Sonarr writes them: FileSystemController and
// SeriesFolderController build them in place, so the document has no name for them.
var sonarrAnswerRecords = pandorest.DeclaredSchemas(pandorest.About{
	Name: "sonarr-answer-records", Service: sonarr,
	Bug: "the records a folder listing, the media files in a folder, a path's type and a series' folder name answer in are not in the document",
}, map[string]*openapi.Schema{
	"FileSystemResource": {
		Type:        openapi.TypeObject,
		Description: "What a folder holds: its folders, its files if they were asked for, and the folder above it, which the top has none of.",
		Properties: map[string]*openapi.Schema{
			"parent":      {Type: openapi.TypeString, Nullable: true},
			"directories": {Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + "FileSystemEntryResource"}},
			"files":       {Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + "FileSystemEntryResource"}},
		},
	},
	"FileSystemEntryResource": {
		Type:        openapi.TypeObject,
		Description: "One folder or file in a listing.",
		Properties: map[string]*openapi.Schema{
			"type":         {Type: openapi.TypeString, Description: "folder, file, drive or parent"},
			"name":         {Type: openapi.TypeString},
			"path":         {Type: openapi.TypeString},
			"extension":    {Type: openapi.TypeString, Nullable: true, Description: "a file's, with its dot"},
			"size":         {Type: openapi.TypeInteger, Format: "int64"},
			"lastModified": {Type: openapi.TypeString, Format: "date-time", Nullable: true},
		},
	},
	"MediaFileResource": {
		Type:        openapi.TypeObject,
		Description: "A video file under a folder, with its path from that folder.",
		Properties: map[string]*openapi.Schema{
			"path":         {Type: openapi.TypeString},
			"relativePath": {Type: openapi.TypeString},
			"name":         {Type: openapi.TypeString},
		},
	},
	"FileSystemTypeResource": {
		Type:        openapi.TypeObject,
		Description: "What a path is.",
		Properties: map[string]*openapi.Schema{
			"type": {Type: openapi.TypeString, Description: "folder or file"},
		},
	},
	"SeriesFolderResource": {
		Type:        openapi.TypeObject,
		Description: "The folder name the series folder format makes for a series.",
		Properties: map[string]*openapi.Schema{
			"folder": {Type: openapi.TypeString},
		},
	},
})

// sonarrUndeclaredResponses says what the GETs that declare no answer give: JSON, a file, or a list of one of the document's records.
var sonarrUndeclaredResponses = pandorest.UndeclaredAnswers(pandorest.About{
	Name: "sonarr-undeclared-responses", Service: sonarr,
	Bug: "fourteen GETs declare a 200 with no content, so nothing says whether they answer JSON (and in what shape) or a file",
}, map[string]pandorest.Answer{
	"GET /api":                            pandorest.JSON(), // {"current":"v3","deprecated":[]}
	"GET /api/v3/autotagging/schema":      pandorest.ListOf("AutoTaggingSpecificationSchema"),
	"GET /api/v3/customformat/schema":     pandorest.ListOf("CustomFormatSpecificationSchema"),
	"GET /api/v3/filesystem":              pandorest.Model("FileSystemResource"),
	"GET /api/v3/filesystem/type":         pandorest.Model("FileSystemTypeResource"),
	"GET /api/v3/filesystem/mediafiles":   pandorest.ListOf("MediaFileResource"),
	"GET /api/v3/config/naming/examples":  pandorest.JSON(),
	"GET /api/v3/series/{id}/folder":      pandorest.Model("SeriesFolderResource"),
	"GET /api/v3/system/routes/duplicate": pandorest.JSON(),

	// SystemController.GetRoutes: the routing graph in DOT, as Content(graph, "text/plain"), whatever its [Produces] says
	"GET /api/v3/system/routes":                    pandorest.File("text/plain"),
	"GET /api/v3/log/file/{filename}":              pandorest.File("text/plain"),
	"GET /api/v3/log/file/update/{filename}":       pandorest.File("text/plain"),
	"GET /api/v3/mediacover/{seriesId}/{filename}": pandorest.File("image/*"),
	"GET /feed/v3/calendar/sonarr.ics":             pandorest.File("text/calendar"),
})

// sonarrUndeclaredWriteResponses says what the writes answer whose action returns object or IActionResult, which Swashbuckle documents as
// nothing. Only answers a caller has a use for are declared: the updated records, the grabbed release, the imported series. The ones that
// answer {} are left as they are.
var sonarrUndeclaredWriteResponses = pandorest.UndeclaredAnswers(pandorest.About{
	Name: "sonarr-undeclared-write-responses", Service: sonarr,
	Bug: "writes whose action returns object or IActionResult are documented as answering nothing, though they answer the records they changed",
}, map[string]pandorest.Answer{
	// EpisodeController.SetEpisodesMonitored: Accepted(resources)
	"PUT /api/v3/episode/monitor": pandorest.ListOf("EpisodeResource"),
	// EpisodeFileController.SetQuality and SetPropertiesBulk
	"PUT /api/v3/episodefile/editor": pandorest.ListOf("EpisodeFileResource"),
	"PUT /api/v3/episodefile/bulk":   pandorest.ListOf("EpisodeFileResource"),
	// ManualImportController.ReprocessItems: the items, reprocessed
	"POST /api/v3/manualimport": pandorest.ListOf("ManualImportReprocessResource"),
	// ReleaseController.DownloadRelease: the release it grabbed
	"POST /api/v3/release": pandorest.Model("ReleaseResource"),
	// SeriesEditorController.SaveAll and SeriesImportController.Import
	"PUT /api/v3/series/editor":  pandorest.ListOf("SeriesResource"),
	"POST /api/v3/series/import": pandorest.ListOf("SeriesResource"),
	// QualityDefinitionController.UpdateMany: every definition, updated
	"PUT /api/v3/qualitydefinition/update": pandorest.ListOf("QualityDefinitionResource"),
})

// sonarrCreatedAccepted corrects the success status of the operations that answer 201 Created or 202 Accepted rather than the 200
// documented. The creates are RestController.Created and CreatedAtAction; the updates are RestController.Accepted, AcceptedAtAction and
// the actions that return Accepted(value).
var sonarrCreatedAccepted = pandorest.WrongStatus(pandorest.About{
	Name: "sonarr-created-accepted", Service: sonarr,
	Bug: "every operation is documented as answering 200, but the creates answer 201 Created and the updates 202 Accepted, with the same body",
}, http.StatusOK, map[int][]string{
	http.StatusCreated: {
		"POST /api/v3/autotagging",
		"POST /api/v3/command",
		"POST /api/v3/customfilter",
		"POST /api/v3/customformat",
		"POST /api/v3/delayprofile",
		"POST /api/v3/downloadclient",
		"POST /api/v3/importlist",
		"POST /api/v3/importlistexclusion",
		"POST /api/v3/indexer",
		"POST /api/v3/metadata",
		"POST /api/v3/notification",
		"POST /api/v3/qualityprofile",
		"POST /api/v3/releaseprofile",
		"POST /api/v3/remotepathmapping",
		"POST /api/v3/rootfolder",
		"POST /api/v3/series",
		"POST /api/v3/tag",
	},
	http.StatusAccepted: {
		"POST /api/v3/languageprofile",
		"POST /api/v3/seasonpass",
		"PUT /api/v3/autotagging/{id}",
		"PUT /api/v3/config/downloadclient/{id}",
		"PUT /api/v3/config/host/{id}",
		"PUT /api/v3/config/importlist/{id}",
		"PUT /api/v3/config/indexer/{id}",
		"PUT /api/v3/config/mediamanagement/{id}",
		"PUT /api/v3/config/naming/{id}",
		"PUT /api/v3/config/ui/{id}",
		"PUT /api/v3/customfilter/{id}",
		"PUT /api/v3/customformat/bulk",
		"PUT /api/v3/customformat/{id}",
		"PUT /api/v3/delayprofile/{id}",
		"PUT /api/v3/downloadclient/bulk",
		"PUT /api/v3/downloadclient/{id}",
		"PUT /api/v3/episode/monitor",
		"PUT /api/v3/episode/{id}",
		"PUT /api/v3/episodefile/bulk",
		"PUT /api/v3/episodefile/editor",
		"PUT /api/v3/episodefile/{id}",
		"PUT /api/v3/importlist/bulk",
		"PUT /api/v3/importlist/{id}",
		"PUT /api/v3/importlistexclusion/{id}",
		"PUT /api/v3/indexer/bulk",
		"PUT /api/v3/indexer/{id}",
		"PUT /api/v3/languageprofile/{id}",
		"PUT /api/v3/metadata/{id}",
		"PUT /api/v3/notification/{id}",
		"PUT /api/v3/qualitydefinition/update",
		"PUT /api/v3/qualitydefinition/{id}",
		"PUT /api/v3/qualityprofile/{id}",
		"PUT /api/v3/releaseprofile/{id}",
		"PUT /api/v3/remotepathmapping/{id}",
		"PUT /api/v3/series/editor",
		"PUT /api/v3/series/{id}",
		"PUT /api/v3/tag/{id}",
	},
})

// sonarrHTTPURIString types HttpUri as the string it is on the wire.
type sonarrHTTPURIString struct{}

func (sonarrHTTPURIString) Name() string    { return "sonarr-http-uri-string" }
func (sonarrHTTPURIString) Service() string { return sonarr }
func (sonarrHTTPURIString) Bug() string {
	return "HttpUri is documented as an object of its parts (scheme, host, path...), but Sonarr's serializer writes it as the URL string (STJHttpUriConverter), so a health check's wikiUrl does not decode"
}

func (sonarrHTTPURIString) Apply(spec *openapi.Spec) error {
	s := spec.Components.Schemas["HttpUri"]
	if s == nil || len(s.Properties) == 0 {
		return errors.New("HttpUri is no longer an object in the document")
	}
	spec.Components.Schemas["HttpUri"] = &openapi.Schema{Type: openapi.TypeString, Nullable: true, Description: "a URL"}

	return nil
}

// sonarrCommandBody lets a command carry its own fields.
type sonarrCommandBody struct{}

func (sonarrCommandBody) Name() string    { return "sonarr-command-body" }
func (sonarrCommandBody) Service() string { return sonarr }
func (sonarrCommandBody) Bug() string {
	return "POST /api/v3/command declares CommandResource as its body, but CommandController reads the command's own fields (seriesId, episodeIds, files...) from the top level of the body, where CommandResource has no place for them"
}

func (sonarrCommandBody) Apply(spec *openapi.Spec) error {
	op, err := operation(spec, http.MethodPost, "/api/v3/command")
	if err != nil {
		return err
	}
	if op.RequestBody == nil || len(op.RequestBody.Content) == 0 {
		return errors.New("POST /api/v3/command takes no body")
	}
	for ct, media := range op.RequestBody.Content {
		if media.Schema == nil || media.Schema.RefName() != "CommandResource" {
			return fmt.Errorf("POST /api/v3/command's %s body is no longer CommandResource", ct)
		}
		media.Schema = &openapi.Schema{Type: openapi.TypeObject, Description: "the command: its name and its own fields, at the top level"}
	}

	return nil
}

// sonarrCommandResourceBody keeps a command's own fields when one is read
// back. Like the body a command is started with (sonarrCommandBody), the
// body a command is read back with is the command's own class, carrying the
// fields that command reads - seriesId, seriesIds, files, importMode - none
// of which the Command schema the document names has, so a model of that
// schema drops them without a word.
type sonarrCommandResourceBody struct{}

func (sonarrCommandResourceBody) Name() string    { return "sonarr-command-resource-body" }
func (sonarrCommandResourceBody) Service() string { return sonarr }
func (sonarrCommandResourceBody) Bug() string {
	return "a command's body is declared as the Command schema, which holds none of the fields a command carries (seriesId, seriesIds, files, importMode...): the server serialises the command's own class"
}

func (sonarrCommandResourceBody) Apply(spec *openapi.Spec) error {
	resource := spec.Components.Schemas["CommandResource"]
	if resource == nil {
		return errors.New("CommandResource is not in the document")
	}
	body := resource.Properties["body"]
	if body == nil {
		return errors.New("CommandResource has no body")
	}
	if body.RefName() != "Command" {
		return errors.New("CommandResource.body is no longer the Command schema")
	}
	*body = openapi.Schema{Type: openapi.TypeObject, Description: "the command's own fields, which depend on its name"}

	return nil
}

// The result of testing a provider, which the document leaves out.
const (
	testAllResult     = "ProviderTestAllResult"
	validationFailure = "ValidationFailure"
)

// sonarrTestResults adds the records testing a provider answers: whether it passed, and each reason it did not.
var sonarrTestResults = pandorest.DeclaredSchemas(pandorest.About{
	Name: "sonarr-test-results", Service: sonarr,
	Bug: "the result of testing a provider (ProviderTestAllResult, and the ValidationFailure it lists) is not in the document",
}, map[string]*openapi.Schema{
	validationFailure: {
		Type:        openapi.TypeObject,
		Description: "One reason a provider failed its test: the setting at fault and what is wrong with it.",
		Properties: map[string]*openapi.Schema{
			"propertyName":        {Type: openapi.TypeString, Nullable: true},
			"errorMessage":        {Type: openapi.TypeString, Nullable: true},
			"severity":            {Type: openapi.TypeString, Description: "error, warning or info"},
			"errorCode":           {Type: openapi.TypeString, Nullable: true},
			"infoLink":            {Type: openapi.TypeString, Nullable: true},
			"detailedDescription": {Type: openapi.TypeString, Nullable: true},
			"isWarning":           {Type: openapi.TypeBoolean},
		},
	},
	testAllResult: {
		Type:        openapi.TypeObject,
		Description: "The outcome of testing one provider.",
		Properties: map[string]*openapi.Schema{
			"id":                 {Type: openapi.TypeInteger, Format: "int32"},
			"isValid":            {Type: openapi.TypeBoolean},
			"validationFailures": {Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + validationFailure}},
		},
	},
})

// sonarrTestAllResults says what testing every provider of a kind answers: the result for each, as a 200 when all pass and a 400 when any
// fails. The providers are the ones ProviderControllerBase serves.
var sonarrTestAllResults = pandorest.AlsoAnswers(pandorest.About{
	Name: "sonarr-test-all-results", Service: sonarr,
	Bug: "POST .../testall is documented as answering nothing, but answers each provider's result, with 400 instead of 200 when any provider fails",
}, map[string]map[int]pandorest.Answer{
	"POST /api/v3/downloadclient/testall": testAllAnswers,
	"POST /api/v3/importlist/testall":     testAllAnswers,
	"POST /api/v3/indexer/testall":        testAllAnswers,
	"POST /api/v3/metadata/testall":       testAllAnswers,
	"POST /api/v3/notification/testall":   testAllAnswers,
})

// testAllAnswers is what testing every provider answers, whether they all pass or not.
var testAllAnswers = map[int]pandorest.Answer{http.StatusOK: pandorest.ListOf(testAllResult), http.StatusBadRequest: pandorest.ListOf(testAllResult)}

// sonarrBulkUpdateLists says what the bulk updates answer: every resource they changed, not the one resource documented. They are
// ProviderControllerBase.UpdateProvider for the providers, and CustomFormatController.Update, each declared as ActionResult<TResource> and
// answering Accepted(the list it updated).
var sonarrBulkUpdateLists = pandorest.WrongAnswers(pandorest.About{
	Name: "sonarr-bulk-update-lists", Service: sonarr,
	Bug: "the bulk updates (PUT .../bulk) are documented as answering one resource, but answer the list of every resource they changed, which one resource cannot decode",
}, map[string]pandorest.Correction{
	"PUT /api/v3/customformat/bulk":   pandorest.ListDeclaredAsOne("CustomFormatResource"),
	"PUT /api/v3/downloadclient/bulk": pandorest.ListDeclaredAsOne("DownloadClientResource"),
	"PUT /api/v3/importlist/bulk":     pandorest.ListDeclaredAsOne("ImportListResource"),
	"PUT /api/v3/indexer/bulk":        pandorest.ListDeclaredAsOne("IndexerResource"),
})

// sonarrAirDays marks the air dates the document declares as plain text as the days they are: an episode's, and the one a release title
// names for a daily show, each written "2026-09-19" (Episode.AIR_DATE_FORMAT).
var sonarrAirDays = pandorest.UndeclaredDays(pandorest.About{
	Name: "sonarr-air-days", Service: sonarr,
	Bug: "airDate is declared as plain text, though it is always a day written 2006-01-02, so nothing marks it as a date",
}, "airDate")
