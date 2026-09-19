package workarounds

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/openapi"
)

// The Sonarr document is the one in the release's source tree,
// src/Sonarr.Api.V3/openapi.json, which Swashbuckle writes from the
// controllers. It describes the request side well and the answers poorly:
// every operation is documented as a 200, and an action returning object or
// IActionResult is documented as answering nothing. What follows was read off
// the controllers (Sonarr.Api.V3, Sonarr.Http) and confirmed by the
// integration suite against the same release.

const sonarr = "sonarr"

// sonarrUIRoutes removes the routes that serve the web interface.
type sonarrUIRoutes struct{}

// sonarrUIPaths are the web interface's routes: the single page app, its
// static files, and the forms login.
var sonarrUIPaths = []string{"/", "/{path}", "/content/{path}", "/login", "/logout"}

func (sonarrUIRoutes) Name() string    { return "sonarr-ui-routes" }
func (sonarrUIRoutes) Service() string { return sonarr }
func (sonarrUIRoutes) Bug() string {
	return "the document includes the routes that serve the web interface (the page, its static files, the forms login), which answer HTML to a browser rather than anything an API client reads"
}

func (sonarrUIRoutes) Apply(spec *openapi.Spec) error {
	for _, path := range sonarrUIPaths {
		if spec.Paths[path] == nil {
			return fmt.Errorf("%s is not in the document", path)
		}
		delete(spec.Paths, path)
	}

	return nil
}

// sonarrUndeclaredResponses declares what the GETs without a response schema
// answer. A value with a slash is a file of that type; answersJSON is JSON of
// no declared schema; anything else is the component schema the JSON decodes
// into, and "[]" before it makes a list of them.
type sonarrUndeclaredResponses struct{}

const (
	answersJSON = "json"
	listOf      = "[]"
)

var sonarrUndeclaredGets = map[string]string{
	// JSON
	"/api":                            answersJSON, // {"current":"v3","deprecated":[]}
	"/api/v3/autotagging/schema":      listOf + "AutoTaggingSpecificationSchema",
	"/api/v3/customformat/schema":     listOf + "CustomFormatSpecificationSchema",
	"/api/v3/filesystem":              answersJSON, // {"parent", "directories", "files"}
	"/api/v3/filesystem/type":         answersJSON, // {"type": "folder"}
	"/api/v3/filesystem/mediafiles":   answersJSON, // [{"path", "relativePath", "name"}]
	"/api/v3/config/naming/examples":  answersJSON,
	"/api/v3/series/{id}/folder":      answersJSON, // {"folder": "Series Title (2020)"}
	"/api/v3/system/routes":           answersJSON,
	"/api/v3/system/routes/duplicate": answersJSON,

	// files
	"/api/v3/log/file/{filename}":              "text/plain",
	"/api/v3/log/file/update/{filename}":       "text/plain",
	"/api/v3/mediacover/{seriesId}/{filename}": "image/*",
	"/feed/v3/calendar/sonarr.ics":             "text/calendar",
}

func (sonarrUndeclaredResponses) Name() string    { return "sonarr-undeclared-responses" }
func (sonarrUndeclaredResponses) Service() string { return sonarr }
func (sonarrUndeclaredResponses) Bug() string {
	return "fourteen GETs declare a 200 with no content, so nothing says whether they answer JSON (and in what shape) or a file"
}

func (sonarrUndeclaredResponses) Apply(spec *openapi.Spec) error {
	return declareAnswers(spec, http.MethodGet, sonarrUndeclaredGets)
}

// sonarrUndeclaredWriteResponses declares the JSON the writes answer when
// their action returns object or IActionResult, which Swashbuckle documents as
// nothing. Only answers a caller has a use for are declared: the updated
// records, the grabbed release, the imported series. The ones that answer {}
// are left as they are.
type sonarrUndeclaredWriteResponses struct{}

var sonarrUndeclaredWrites = map[string]string{
	// EpisodeController.SetEpisodesMonitored: Accepted(resources)
	"PUT /api/v3/episode/monitor": listOf + "EpisodeResource",
	// EpisodeFileController.SetQuality and SetPropertiesBulk
	"PUT /api/v3/episodefile/editor": listOf + "EpisodeFileResource",
	"PUT /api/v3/episodefile/bulk":   listOf + "EpisodeFileResource",
	// ManualImportController.ReprocessItems: the items, reprocessed
	"POST /api/v3/manualimport": listOf + "ManualImportReprocessResource",
	// ReleaseController.DownloadRelease: the release it grabbed
	"POST /api/v3/release": "ReleaseResource",
	// SeriesEditorController.SaveAll and SeriesImportController.Import
	"PUT /api/v3/series/editor":  listOf + "SeriesResource",
	"POST /api/v3/series/import": listOf + "SeriesResource",
	// QualityDefinitionController.UpdateMany: every definition, updated
	"PUT /api/v3/qualitydefinition/update": listOf + "QualityDefinitionResource",
}

func (sonarrUndeclaredWriteResponses) Name() string    { return "sonarr-undeclared-write-responses" }
func (sonarrUndeclaredWriteResponses) Service() string { return sonarr }
func (sonarrUndeclaredWriteResponses) Bug() string {
	return "writes whose action returns object or IActionResult are documented as answering nothing, though they answer the records they changed"
}

func (sonarrUndeclaredWriteResponses) Apply(spec *openapi.Spec) error {
	byMethod := map[string]map[string]string{}
	for target, answer := range sonarrUndeclaredWrites {
		method, path, _ := strings.Cut(target, " ")
		if byMethod[method] == nil {
			byMethod[method] = map[string]string{}
		}
		byMethod[method][path] = answer
	}
	for _, method := range openapi.SortedKeys(byMethod) {
		if err := declareAnswers(spec, method, byMethod[method]); err != nil {
			return err
		}
	}

	return nil
}

// declareAnswers gives each path's operation the answer the table names on
// its 200, failing for any that already declares one.
func declareAnswers(spec *openapi.Spec, method string, answers map[string]string) error {
	var declared []string
	for _, path := range openapi.SortedKeys(answers) {
		op, err := operation(spec, method, path)
		if err != nil {
			return err
		}
		ok := op.Responses["200"]
		if ok == nil {
			return fmt.Errorf("%s %s has no 200 response", method, path)
		}
		if len(ok.Content) > 0 {
			declared = append(declared, method+" "+path)
			continue
		}
		content, err := answerContent(spec, answers[path])
		if err != nil {
			return fmt.Errorf("%s %s: %w", method, path, err)
		}
		ok.Content = content
	}
	if len(declared) > 0 {
		return fmt.Errorf("these now declare their response, so take them out of the table: %s", strings.Join(declared, ", "))
	}

	return nil
}

// answerContent is the content map for one table entry.
func answerContent(spec *openapi.Spec, answer string) (map[string]*openapi.MediaType, error) {
	switch {
	case answer == answersJSON:
		return map[string]*openapi.MediaType{"application/json": {}}, nil
	case strings.Contains(answer, "/"):
		return map[string]*openapi.MediaType{answer: {Schema: &openapi.Schema{Type: openapi.TypeString, Format: "binary"}}}, nil
	}
	name, list := strings.CutPrefix(answer, listOf)
	if spec.Components.Schemas[name] == nil {
		return nil, fmt.Errorf("schema %s is not in the document", name)
	}
	schema := &openapi.Schema{Ref: openapi.SchemaRefPrefix + name}
	if list {
		schema = &openapi.Schema{Type: openapi.TypeArray, Items: schema}
	}

	return map[string]*openapi.MediaType{"application/json": {Schema: schema}}, nil
}

// sonarrCreatedAccepted corrects the success status of the operations that
// answer 201 Created or 202 Accepted rather than the 200 documented.
type sonarrCreatedAccepted struct{}

// sonarrCreated are the creates: RestController.Created, CreatedAtAction.
var sonarrCreated = []string{
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
}

// sonarrAccepted are the updates: RestController.Accepted, AcceptedAtAction,
// and the actions that return Accepted(value).
var sonarrAccepted = []string{
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
}

func (sonarrCreatedAccepted) Name() string    { return "sonarr-created-accepted" }
func (sonarrCreatedAccepted) Service() string { return sonarr }
func (sonarrCreatedAccepted) Bug() string {
	return "every operation is documented as answering 200, but the creates answer 201 Created and the updates 202 Accepted, with the same body"
}

func (sonarrCreatedAccepted) Apply(spec *openapi.Spec) error {
	for _, set := range []struct {
		status  string
		targets []string
	}{{"201", sonarrCreated}, {"202", sonarrAccepted}} {
		for _, target := range set.targets {
			method, path, _ := strings.Cut(target, " ")
			op, err := operation(spec, method, path)
			if err != nil {
				return err
			}
			ok := op.Responses["200"]
			if ok == nil || op.Responses[set.status] != nil {
				return fmt.Errorf("%s no longer documents only a 200", target)
			}
			delete(op.Responses, "200")
			op.Responses[set.status] = ok
		}
	}

	return nil
}

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

// sonarrTestAllResults declares what testing every provider of a kind
// answers: the result for each, as a 200 when all pass and a 400 when any
// fails.
type sonarrTestAllResults struct{}

// sonarrTestAll are the providers ProviderControllerBase serves.
var sonarrTestAll = []string{
	"/api/v3/downloadclient/testall",
	"/api/v3/importlist/testall",
	"/api/v3/indexer/testall",
	"/api/v3/metadata/testall",
	"/api/v3/notification/testall",
}

const (
	testAllResult     = "ProviderTestAllResult"
	validationFailure = "ValidationFailure"
)

func (sonarrTestAllResults) Name() string    { return "sonarr-test-all-results" }
func (sonarrTestAllResults) Service() string { return sonarr }
func (sonarrTestAllResults) Bug() string {
	return "POST .../testall is documented as answering nothing, but answers each provider's result (ProviderTestAllResult, not in the document), with 400 instead of 200 when any provider fails"
}

func (sonarrTestAllResults) Apply(spec *openapi.Spec) error {
	for _, name := range []string{testAllResult, validationFailure} {
		if spec.Components.Schemas[name] != nil {
			return fmt.Errorf("the document declares %s", name)
		}
	}
	str := func() *openapi.Schema { return &openapi.Schema{Type: openapi.TypeString, Nullable: true} }
	spec.Components.Schemas[validationFailure] = &openapi.Schema{
		Type:        openapi.TypeObject,
		Description: "One reason a provider failed its test: the setting at fault and what is wrong with it.",
		Properties: map[string]*openapi.Schema{
			"propertyName":        str(),
			"errorMessage":        str(),
			"severity":            {Type: openapi.TypeString, Description: "error, warning or info"},
			"errorCode":           str(),
			"infoLink":            str(),
			"detailedDescription": str(),
			"isWarning":           {Type: openapi.TypeBoolean},
		},
	}
	spec.Components.Schemas[testAllResult] = &openapi.Schema{
		Type:        openapi.TypeObject,
		Description: "The outcome of testing one provider.",
		Properties: map[string]*openapi.Schema{
			"id":                 {Type: openapi.TypeInteger, Format: "int32"},
			"isValid":            {Type: openapi.TypeBoolean},
			"validationFailures": {Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + validationFailure}},
		},
	}
	results := func() map[string]*openapi.MediaType {
		return map[string]*openapi.MediaType{"application/json": {Schema: &openapi.Schema{
			Type: openapi.TypeArray, Items: &openapi.Schema{Ref: openapi.SchemaRefPrefix + testAllResult},
		}}}
	}
	for _, path := range sonarrTestAll {
		op, err := operation(spec, http.MethodPost, path)
		if err != nil {
			return err
		}
		ok := op.Responses["200"]
		switch {
		case ok == nil:
			return fmt.Errorf("POST %s has no 200 response", path)
		case len(ok.Content) > 0 || op.Responses["400"] != nil:
			return fmt.Errorf("POST %s declares its answer", path)
		}
		ok.Content = results()
		op.Responses["400"] = &openapi.Response{Description: "At least one provider failed its test", Content: results()}
	}

	return nil
}
