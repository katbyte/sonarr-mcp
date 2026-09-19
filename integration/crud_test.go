//go:build integration

package integration

// Create, read, update and delete for every resource Sonarr lets a caller
// configure. Each create must answer 201 and each update 202, which is what
// the sonarr-created-accepted workaround claims; the client would already
// fail a call answered in a status it does not expect, and status() says in
// each test which status that was.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/lib/client"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

// gone checks a read of something deleted answers 404.
func gone(t *testing.T, what string, err error) {
	t.Helper()

	if !client.IsNotFound(err) {
		t.Errorf("%s after its delete: %v, want a 404", what, err)
	}
}

// goneFromCache checks a read of something deleted fails the way Sonarr's
// cached lookups fail: CustomFormatService.GetById and
// AutoTaggingService.GetById index a dictionary of everything, and an id that
// is not in it throws KeyNotFoundException, which Sonarr answers with a 500
// rather than the 404 its other resources give.
func goneFromCache(t *testing.T, what string, err error) {
	t.Helper()

	var se *client.StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusInternalServerError || !strings.Contains(err.Error(), "was not present in the dictionary") {
		t.Errorf("%s after its delete: %v, want Sonarr's 500 for an id its cache lacks", what, err)
	}
}

// newTag makes a tag for a test, deleted when the test ends.
func newTag(ctx context.Context, t *testing.T, label string) int {
	t.Helper()

	res := must(sc.PostTag(ctx, sonarr.TagResource{Label: label}))
	status(t, res.HttpResponse, http.StatusCreated)
	id := res.Model.Id
	t.Cleanup(func() { _, _ = sc.DeleteTagById(context.WithoutCancel(ctx), id) })

	return id
}

//nolint:paralleltest // the tests share one Sonarr
func TestTags(t *testing.T) {
	ctx := skipUnlessUp(t)

	res := must(sc.PostTag(ctx, sonarr.TagResource{Label: "SDK-Tag"}))
	status(t, res.HttpResponse, http.StatusCreated)
	tag := *res.Model
	// Sonarr stores labels lower case
	if tag.Id == 0 || tag.Label != "sdk-tag" {
		t.Fatalf("PostTag = %+v", tag)
	}
	if got := must(sc.GetTagById(ctx, tag.Id)).Model; got.Label != "sdk-tag" {
		t.Errorf("GetTagById = %+v", got)
	}

	tag.Label = "sdk-tag-renamed"
	upd := must(sc.PutTagById(ctx, strconv.Itoa(tag.Id), tag))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if upd.Model.Label != "sdk-tag-renamed" {
		t.Errorf("PutTagById = %+v", upd.Model)
	}
	if !slices.ContainsFunc(must(sc.GetTag(ctx)).Model, func(x sonarr.TagResource) bool { return x.Id == tag.Id && x.Label == "sdk-tag-renamed" }) {
		t.Error("GetTag does not list the renamed tag")
	}

	// the details say what carries a tag: here, nothing yet, then a series
	detail := must(sc.GetTagDetailById(ctx, tag.Id)).Model
	if detail.Label != "sdk-tag-renamed" || len(detail.SeriesIds) != 0 {
		t.Errorf("GetTagDetailById = %+v", detail)
	}
	s := seriesByTitle(ctx, t, severance.Title)
	s.Tags = append(slices.Clone(s.Tags), tag.Id)
	must(sc.PutSeriesById(ctx, strconv.Itoa(s.Id), s, sonarr.PutSeriesByIdOperationOptions{}))
	details := must(sc.GetTagDetail(ctx)).Model
	i := slices.IndexFunc(details, func(d sonarr.TagDetailsResource) bool { return d.Id == tag.Id })
	if i < 0 || !slices.Contains(details[i].SeriesIds, s.Id) {
		t.Errorf("GetTagDetail = %+v, want the tag on %s", details, s.Title)
	}
	s.Tags = slices.DeleteFunc(s.Tags, func(id int) bool { return id == tag.Id })
	must(sc.PutSeriesById(ctx, strconv.Itoa(s.Id), s, sonarr.PutSeriesByIdOperationOptions{}))

	status(t, must(sc.DeleteTagById(ctx, tag.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetTagById(ctx, tag.Id)
	gone(t, "GetTagById", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestRootFolders(t *testing.T) {
	ctx := skipUnlessUp(t)

	// a root folder has to exist, and be writable, in the container
	mkdirAll(t, dataDir(), "downloads", "sdk-root")
	res := must(sc.PostRootFolder(ctx, sonarr.RootFolderResource{Path: "/downloads/sdk-root"}))
	status(t, res.HttpResponse, http.StatusCreated)
	id := res.Model.Id
	if res.Model.Path != "/downloads/sdk-root" || res.Model.Accessible == nil || !*res.Model.Accessible || res.Model.FreeSpace <= 0 {
		t.Errorf("PostRootFolder = %+v", res.Model)
	}
	if got := must(sc.GetRootFolderById(ctx, id)).Model; got.Path != "/downloads/sdk-root" {
		t.Errorf("GetRootFolderById = %+v", got)
	}

	// the library's root lists the folder no series lives in
	tv := must(sc.GetRootFolderById(ctx, rootFolderID)).Model
	if !slices.ContainsFunc(tv.UnmappedFolders, func(u sonarr.UnmappedFolder) bool {
		return u.Name == theExpanse.Folder && u.Path == "/tv/"+theExpanse.Folder
	}) {
		t.Errorf("/tv's unmapped folders = %+v, want %s", tv.UnmappedFolders, theExpanse.Folder)
	}

	// the same folder twice is refused, field by field
	_, err := sc.PostRootFolder(ctx, sonarr.RootFolderResource{Path: "/downloads/sdk-root"})
	if !isStatus(err, http.StatusBadRequest) {
		t.Errorf("a second PostRootFolder for the same path = %v, want a 400", err)
	}

	status(t, must(sc.DeleteRootFolderById(ctx, id)).HttpResponse, http.StatusOK)
	_, err = sc.GetRootFolderById(ctx, id)
	gone(t, "GetRootFolderById", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestQualityProfiles(t *testing.T) {
	ctx := skipUnlessUp(t)

	// a new profile starts from the schema: every quality, none allowed
	schema := *must(sc.GetQualityProfileSchema(ctx)).Model
	if len(schema.Items) == 0 {
		t.Fatalf("GetQualityProfileSchema = %+v", schema)
	}
	allowed := 0
	for i := range schema.Items {
		it := &schema.Items[i]
		if it.Quality != nil && (it.Quality.Name == "SDTV" || it.Quality.Name == "HDTV-720p") {
			it.Allowed = new(true)
			allowed++
			if it.Quality.Name == "HDTV-720p" {
				schema.Cutoff = it.Quality.Id
			}
		}
	}
	if allowed != 2 {
		t.Fatalf("the schema lacks SDTV or HDTV-720p: %+v", schema.Items)
	}
	schema.Name, schema.UpgradeAllowed = "SDK Profile", new(true)
	res := must(sc.PostQualityProfile(ctx, schema))
	status(t, res.HttpResponse, http.StatusCreated)
	profile := *res.Model
	t.Cleanup(func() { _, _ = sc.DeleteQualityProfileById(context.WithoutCancel(ctx), profile.Id) })
	if profile.Id == 0 || profile.Name != "SDK Profile" || !*profile.UpgradeAllowed {
		t.Fatalf("PostQualityProfile = %+v", profile)
	}

	profile.Name, profile.UpgradeAllowed = "SDK Profile Renamed", new(false)
	upd := must(sc.PutQualityProfileById(ctx, strconv.Itoa(profile.Id), profile))
	status(t, upd.HttpResponse, http.StatusAccepted)
	got := must(sc.GetQualityProfileById(ctx, profile.Id)).Model
	if got.Name != "SDK Profile Renamed" || *got.UpgradeAllowed {
		t.Errorf("GetQualityProfileById after the update = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetQualityProfile(ctx)).Model, func(p sonarr.QualityProfileResource) bool { return p.Id == profile.Id }) {
		t.Error("GetQualityProfile does not list the new profile")
	}

	status(t, must(sc.DeleteQualityProfileById(ctx, profile.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetQualityProfileById(ctx, profile.Id)
	gone(t, "GetQualityProfileById", err)

	// a profile a series is on cannot be deleted
	_, err = sc.DeleteQualityProfileById(ctx, profileID)
	if client.StatusCode(err) < 400 {
		t.Errorf("deleting the profile the library is on = %v, want a refusal", err)
	}
}

//nolint:paralleltest // the tests share one Sonarr
func TestQualityDefinitions(t *testing.T) {
	ctx := skipUnlessUp(t)

	defs := must(sc.GetQualityDefinition(ctx)).Model
	i := slices.IndexFunc(defs, func(d sonarr.QualityDefinitionResource) bool {
		return d.Quality != nil && d.Quality.Name == "HDTV-720p"
	})
	if i < 0 {
		t.Fatalf("GetQualityDefinition has no HDTV-720p: %+v", defs)
	}
	def := defs[i]
	if def.Id == 0 || def.Title == "" || def.Weight == 0 || def.MaxSize == 0 {
		t.Errorf("the HDTV-720p definition did not decode: %+v", def)
	}
	if got := must(sc.GetQualityDefinitionById(ctx, def.Id)).Model; got.Title != def.Title {
		t.Errorf("GetQualityDefinitionById = %+v", got)
	}
	limits := must(sc.GetQualityDefinitionLimits(ctx)).Model
	if limits.Max <= limits.Min {
		t.Errorf("GetQualityDefinitionLimits = %+v", limits)
	}

	original := def.Title
	def.Title = "SDK 720p"
	upd := must(sc.PutQualityDefinitionById(ctx, strconv.Itoa(def.Id), def))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if upd.Model.Title != "SDK 720p" {
		t.Errorf("PutQualityDefinitionById = %+v", upd.Model)
	}

	// the bulk update answers every definition - but as Sonarr cached them:
	// QualityDefinitionService.UpdateMany writes the database and, unlike
	// Update, leaves the cache the answer is read from as it was
	def.Title = original
	many := must(sc.PutQualityDefinitionUpdate(ctx, []sonarr.QualityDefinitionResource{def}))
	status(t, many.HttpResponse, http.StatusAccepted)
	if len(many.Model) != len(defs) || !slices.ContainsFunc(many.Model, func(d sonarr.QualityDefinitionResource) bool { return d.Id == def.Id }) {
		t.Errorf("PutQualityDefinitionUpdate = %d definitions, want all %d", len(many.Model), len(defs))
	}
	// an update of one clears the cache, and puts the title back for certain
	must(sc.PutQualityDefinitionById(ctx, strconv.Itoa(def.Id), def))
	if got := must(sc.GetQualityDefinitionById(ctx, def.Id)).Model; got.Title != original {
		t.Errorf("the definition's title is %q after restoring it, want %q", got.Title, original)
	}
}

// formatSpec is the custom format specification of an implementation, from
// Sonarr's own schema, with its value set.
func formatSpec(t *testing.T, schema []sonarr.CustomFormatSpecificationSchema, implementation, name string, value any) sonarr.CustomFormatSpecificationSchema {
	t.Helper()

	i := slices.IndexFunc(schema, func(s sonarr.CustomFormatSpecificationSchema) bool { return s.Implementation == implementation })
	if i < 0 {
		t.Fatalf("the custom format schema has no %s", implementation)
	}
	s := schema[i]
	s.Name, s.Fields, s.Negate, s.Required = name, fields(s.Fields, map[string]any{"value": value}), new(false), new(false)

	return s
}

// tagSpec is the same for an auto tagging specification.
func tagSpec(t *testing.T, schema []sonarr.AutoTaggingSpecificationSchema, implementation, name string, value any) sonarr.AutoTaggingSpecificationSchema {
	t.Helper()

	i := slices.IndexFunc(schema, func(s sonarr.AutoTaggingSpecificationSchema) bool { return s.Implementation == implementation })
	if i < 0 {
		t.Fatalf("the auto tagging schema has no %s", implementation)
	}
	s := schema[i]
	s.Name, s.Fields, s.Negate, s.Required = name, fields(s.Fields, map[string]any{"value": value}), new(false), new(false)

	return s
}

//nolint:paralleltest // the tests share one Sonarr
func TestCustomFormats(t *testing.T) {
	ctx := skipUnlessUp(t)

	schema := must(sc.GetCustomFormatSchema(ctx)).Model
	x265 := formatSpec(t, schema, "ReleaseTitleSpecification", "x265", `\b(x265|HEVC)\b`)
	create := func(name string) sonarr.CustomFormatResource {
		res := must(sc.PostCustomFormat(ctx, sonarr.CustomFormatResource{
			Name: name, IncludeCustomFormatWhenRenaming: new(false), Specifications: []sonarr.CustomFormatSpecificationSchema{x265},
		}))
		status(t, res.HttpResponse, http.StatusCreated)
		id := res.Model.Id
		t.Cleanup(func() { _, _ = sc.DeleteCustomFormatById(context.WithoutCancel(ctx), id) })
		return *res.Model
	}

	cf := create("SDK x265")
	if cf.Id == 0 || len(cf.Specifications) != 1 || fieldValue(cf.Specifications[0].Fields, "value") != `\b(x265|HEVC)\b` {
		t.Fatalf("PostCustomFormat = %+v", cf)
	}
	cf.Name = "SDK HEVC"
	upd := must(sc.PutCustomFormatById(ctx, strconv.Itoa(cf.Id), cf))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if got := must(sc.GetCustomFormatById(ctx, cf.Id)).Model; got.Name != "SDK HEVC" {
		t.Errorf("GetCustomFormatById after the update = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetCustomFormat(ctx)).Model, func(x sonarr.CustomFormatResource) bool { return x.Id == cf.Id }) {
		t.Error("GetCustomFormat does not list the new format")
	}

	// the bulk update answers every format it changed
	other := create("SDK AV1")
	bulk := must(sc.PutCustomFormatBulk(ctx, sonarr.CustomFormatBulkResource{Ids: []int{cf.Id, other.Id}, IncludeCustomFormatWhenRenaming: new(true)}))
	status(t, bulk.HttpResponse, http.StatusAccepted)
	if len(bulk.Model) != 2 || !slices.ContainsFunc(bulk.Model, func(x sonarr.CustomFormatResource) bool {
		return x.Id == other.Id && x.IncludeCustomFormatWhenRenaming != nil && *x.IncludeCustomFormatWhenRenaming
	}) {
		t.Errorf("PutCustomFormatBulk = %+v", bulk.Model)
	}

	status(t, must(sc.DeleteCustomFormatById(ctx, cf.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetCustomFormatById(ctx, cf.Id)
	goneFromCache(t, "GetCustomFormatById", err)
	status(t, must(sc.DeleteCustomFormatBulk(ctx, sonarr.CustomFormatBulkResource{Ids: []int{other.Id}})).HttpResponse, http.StatusOK)
	_, err = sc.GetCustomFormatById(ctx, other.Id)
	goneFromCache(t, "GetCustomFormatById after the bulk delete", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestDelayProfiles(t *testing.T) {
	ctx := skipUnlessUp(t)

	// the default profile, with no tags, is always last
	defaults := must(sc.GetDelayProfile(ctx)).Model
	if len(defaults) == 0 {
		t.Fatal("GetDelayProfile listed no default profile")
	}

	// a profile besides the default has to name the tags it applies to
	create := func(label string) sonarr.DelayProfileResource {
		tag := newTag(ctx, t, label)
		res := must(sc.PostDelayProfile(ctx, sonarr.DelayProfileResource{
			EnableUsenet: new(true), EnableTorrent: new(true), PreferredProtocol: sonarr.DownloadProtocolUsenet,
			UsenetDelay: 30, TorrentDelay: 60, BypassIfHighestQuality: new(true), Tags: []int{tag},
		}))
		status(t, res.HttpResponse, http.StatusCreated)
		id := res.Model.Id
		t.Cleanup(func() { _, _ = sc.DeleteDelayProfileById(context.WithoutCancel(ctx), id) })
		return *res.Model
	}
	a, b := create("sdk-delay-a"), create("sdk-delay-b")
	if a.Id == 0 || a.UsenetDelay != 30 || a.TorrentDelay != 60 || a.Order == 0 {
		t.Fatalf("PostDelayProfile = %+v", a)
	}

	a.UsenetDelay = 90
	upd := must(sc.PutDelayProfileById(ctx, strconv.Itoa(a.Id), a))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if got := must(sc.GetDelayProfileById(ctx, a.Id)).Model; got.UsenetDelay != 90 {
		t.Errorf("GetDelayProfileById after the update = %+v", got)
	}

	// moving a after b answers the whole ordered list, a now below b
	order := must(sc.PutDelayProfileReorderById(ctx, a.Id, sonarr.PutDelayProfileReorderByIdOperationOptions{After: b.Id}))
	status(t, order.HttpResponse, http.StatusOK)
	ia := slices.IndexFunc(order.Model, func(p sonarr.DelayProfileResource) bool { return p.Id == a.Id })
	ib := slices.IndexFunc(order.Model, func(p sonarr.DelayProfileResource) bool { return p.Id == b.Id })
	if ia < 0 || ib < 0 || order.Model[ia].Order <= order.Model[ib].Order {
		t.Errorf("PutDelayProfileReorderById = %+v, want %d after %d", order.Model, a.Id, b.Id)
	}

	status(t, must(sc.DeleteDelayProfileById(ctx, a.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetDelayProfileById(ctx, a.Id)
	gone(t, "GetDelayProfileById", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestReleaseProfiles(t *testing.T) {
	ctx := skipUnlessUp(t)

	res := must(sc.PostReleaseProfile(ctx, sonarr.ReleaseProfileResource{
		Name: "SDK Release Profile", Enabled: new(true), Required: []string{"proper"}, Ignored: []string{"cam", "telesync"},
	}))
	status(t, res.HttpResponse, http.StatusCreated)
	rp := *res.Model
	t.Cleanup(func() { _, _ = sc.DeleteReleaseProfileById(context.WithoutCancel(ctx), rp.Id) })
	if rp.Id == 0 || rp.Name != "SDK Release Profile" {
		t.Fatalf("PostReleaseProfile = %+v", rp)
	}
	// the terms are free-form JSON in the document: a list of strings here
	if ignored, ok := rp.Ignored.([]any); !ok || len(ignored) != 2 {
		t.Errorf("the ignored terms are %T %v, want the two sent", rp.Ignored, rp.Ignored)
	}

	rp.Enabled = new(false)
	upd := must(sc.PutReleaseProfileById(ctx, strconv.Itoa(rp.Id), rp))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if got := must(sc.GetReleaseProfileById(ctx, rp.Id)).Model; got.Enabled == nil || *got.Enabled {
		t.Errorf("GetReleaseProfileById after disabling = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetReleaseProfile(ctx)).Model, func(x sonarr.ReleaseProfileResource) bool { return x.Id == rp.Id }) {
		t.Error("GetReleaseProfile does not list the new profile")
	}

	status(t, must(sc.DeleteReleaseProfileById(ctx, rp.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetReleaseProfileById(ctx, rp.Id)
	gone(t, "GetReleaseProfileById", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestRemotePathMappings(t *testing.T) {
	ctx := skipUnlessUp(t)

	res := must(sc.PostRemotePathMapping(ctx, sonarr.RemotePathMappingResource{Host: "sdk-host", RemotePath: "/remote/tv/", LocalPath: "/downloads/"}))
	status(t, res.HttpResponse, http.StatusCreated)
	m := *res.Model
	t.Cleanup(func() { _, _ = sc.DeleteRemotePathMappingById(context.WithoutCancel(ctx), m.Id) })
	if m.Id == 0 || m.Host != "sdk-host" || m.LocalPath != "/downloads/" {
		t.Fatalf("PostRemotePathMapping = %+v", m)
	}

	m.RemotePath = "/remote/series/"
	upd := must(sc.PutRemotePathMappingById(ctx, strconv.Itoa(m.Id), m))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if got := must(sc.GetRemotePathMappingById(ctx, m.Id)).Model; got.RemotePath != "/remote/series/" {
		t.Errorf("GetRemotePathMappingById after the update = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetRemotePathMapping(ctx)).Model, func(x sonarr.RemotePathMappingResource) bool { return x.Id == m.Id }) {
		t.Error("GetRemotePathMapping does not list the new mapping")
	}

	status(t, must(sc.DeleteRemotePathMappingById(ctx, m.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetRemotePathMappingById(ctx, m.Id)
	gone(t, "GetRemotePathMappingById", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestImportListExclusions(t *testing.T) {
	ctx := skipUnlessUp(t)

	create := func(tvdb int, title string) sonarr.ImportListExclusionResource {
		res := must(sc.PostImportListExclusion(ctx, sonarr.ImportListExclusionResource{TvdbId: tvdb, Title: title}))
		status(t, res.HttpResponse, http.StatusCreated)
		id := res.Model.Id
		t.Cleanup(func() { _, _ = sc.DeleteImportListExclusionById(context.WithoutCancel(ctx), id) })
		return *res.Model
	}
	ex := create(74205, "Band of Brothers")
	if ex.Id == 0 || ex.TvdbId != 74205 {
		t.Fatalf("PostImportListExclusion = %+v", ex)
	}

	ex.Title = "Band of Brothers (2001)"
	upd := must(sc.PutImportListExclusionById(ctx, strconv.Itoa(ex.Id), ex))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if got := must(sc.GetImportListExclusionById(ctx, ex.Id)).Model; got.Title != "Band of Brothers (2001)" {
		t.Errorf("GetImportListExclusionById after the update = %+v", got)
	}
	// the whole list is deprecated for the paged one, and still served
	all := must(sc.GetImportListExclusion(ctx)).Model //nolint:staticcheck // the suite proves the deprecated operation still answers
	if !slices.ContainsFunc(all, func(x sonarr.ImportListExclusionResource) bool { return x.Id == ex.Id }) {
		t.Error("GetImportListExclusion does not list the new exclusion")
	}
	other := create(78107, "The Office")
	paged := must(sc.GetImportListExclusionPagedComplete(ctx, sonarr.GetImportListExclusionPagedOperationOptions{PageSize: 1}))
	if len(paged.Items) < 2 || !slices.ContainsFunc(paged.Items, func(x sonarr.ImportListExclusionResource) bool { return x.Id == other.Id }) {
		t.Errorf("GetImportListExclusionPagedComplete over pages of one = %+v", paged.Items)
	}

	status(t, must(sc.DeleteImportListExclusionById(ctx, ex.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetImportListExclusionById(ctx, ex.Id)
	gone(t, "GetImportListExclusionById", err)
	status(t, must(sc.DeleteImportListExclusionBulk(ctx, sonarr.ImportListExclusionBulkResource{Ids: []int{other.Id}})).HttpResponse, http.StatusOK)
	_, err = sc.GetImportListExclusionById(ctx, other.Id)
	gone(t, "GetImportListExclusionById after the bulk delete", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestCustomFilters(t *testing.T) {
	ctx := skipUnlessUp(t)

	res := must(sc.PostCustomFilter(ctx, sonarr.CustomFilterResource{
		Type: "series", Label: "SDK Filter", Filters: []map[string]any{{"key": "monitored", "value": []any{true}, "type": "equal"}},
	}))
	status(t, res.HttpResponse, http.StatusCreated)
	f := *res.Model
	t.Cleanup(func() { _, _ = sc.DeleteCustomFilterById(context.WithoutCancel(ctx), f.Id) })
	if f.Id == 0 || f.Label != "SDK Filter" || len(f.Filters) != 1 || f.Filters[0]["key"] != "monitored" {
		t.Fatalf("PostCustomFilter = %+v", f)
	}

	f.Label = "SDK Filter Renamed"
	upd := must(sc.PutCustomFilterById(ctx, strconv.Itoa(f.Id), f))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if got := must(sc.GetCustomFilterById(ctx, f.Id)).Model; got.Label != "SDK Filter Renamed" {
		t.Errorf("GetCustomFilterById after the update = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetCustomFilter(ctx)).Model, func(x sonarr.CustomFilterResource) bool { return x.Id == f.Id }) {
		t.Error("GetCustomFilter does not list the new filter")
	}

	status(t, must(sc.DeleteCustomFilterById(ctx, f.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetCustomFilterById(ctx, f.Id)
	gone(t, "GetCustomFilterById", err)
}

//nolint:paralleltest // the tests share one Sonarr
func TestAutoTagging(t *testing.T) {
	ctx := skipUnlessUp(t)

	tag := newTag(ctx, t, "sdk-anime")
	schema := must(sc.GetAutoTaggingSchema(ctx)).Model
	genre := tagSpec(t, schema, "GenreSpecification", "Anime", []string{"Anime"})
	res := must(sc.PostAutoTagging(ctx, sonarr.AutoTaggingResource{
		Name: "SDK Anime", RemoveTagsAutomatically: new(false), Tags: []int{tag}, Specifications: []sonarr.AutoTaggingSpecificationSchema{genre},
	}))
	status(t, res.HttpResponse, http.StatusCreated)
	at := *res.Model
	t.Cleanup(func() { _, _ = sc.DeleteAutoTaggingById(context.WithoutCancel(ctx), at.Id) })
	if at.Id == 0 || at.Name != "SDK Anime" || len(at.Specifications) != 1 || !slices.Contains(at.Tags, tag) {
		t.Fatalf("PostAutoTagging = %+v", at)
	}

	at.RemoveTagsAutomatically = new(true)
	upd := must(sc.PutAutoTaggingById(ctx, strconv.Itoa(at.Id), at))
	status(t, upd.HttpResponse, http.StatusAccepted)
	if got := must(sc.GetAutoTaggingById(ctx, at.Id)).Model; got.RemoveTagsAutomatically == nil || !*got.RemoveTagsAutomatically {
		t.Errorf("GetAutoTaggingById after the update = %+v", got)
	}
	if !slices.ContainsFunc(must(sc.GetAutoTagging(ctx)).Model, func(x sonarr.AutoTaggingResource) bool { return x.Id == at.Id }) {
		t.Error("GetAutoTagging does not list the new rule")
	}

	status(t, must(sc.DeleteAutoTaggingById(ctx, at.Id)).HttpResponse, http.StatusOK)
	_, err := sc.GetAutoTaggingById(ctx, at.Id)
	goneFromCache(t, "GetAutoTaggingById", err)
}

// Language profiles are gone from Sonarr 4 - languages are custom formats
// now - but the routes stay for old clients, answering one fixed profile and
// accepting writes they ignore.
//
//nolint:paralleltest,staticcheck // the tests share one Sonarr; the suite proves the deprecated operations still answer
func TestLanguageProfiles(t *testing.T) {
	ctx := skipUnlessUp(t)

	list := must(sc.GetLanguageProfile(ctx)).Model
	if len(list) != 1 || list[0].Name == "" {
		t.Fatalf("GetLanguageProfile = %+v", list)
	}
	one := *must(sc.GetLanguageProfileById(ctx, list[0].Id)).Model
	if one.Id != list[0].Id || one.Cutoff == nil {
		t.Errorf("GetLanguageProfileById = %+v", one)
	}
	if schema := must(sc.GetLanguageProfileSchema(ctx)).Model; schema == nil || schema.Name == "" {
		t.Errorf("GetLanguageProfileSchema = %+v", schema)
	}

	res := must(sc.PostLanguageProfile(ctx, one))
	status(t, res.HttpResponse, http.StatusAccepted)
	upd := must(sc.PutLanguageProfileById(ctx, strconv.Itoa(one.Id), one))
	status(t, upd.HttpResponse, http.StatusAccepted)
	status(t, must(sc.DeleteLanguageProfileById(ctx, one.Id)).HttpResponse, http.StatusOK)
	// and the profile is still there, since none of that did anything
	if len(must(sc.GetLanguageProfile(ctx)).Model) != 1 {
		t.Error("the deprecated profile routes changed something")
	}
}
