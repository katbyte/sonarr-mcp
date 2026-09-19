//go:build integration

package integration

// The read sweep: every GET operation in the definitions, called against the
// running Sonarr with arguments resolved from the fixtures, and its answer
// decoded (or its file read). The bespoke tests prove the shapes the tools
// rely on field by field; the sweep proves the rest of the read surface
// answers in its documented status and shape, and that it keeps doing so as
// the definitions change: a GET the importer adds is swept on the next run
// with nothing to write, and one that fails must be classified here.
//
// Every operation either answers, or has a sweepCase that says why not. A
// case whose operation starts answering fails the sweep, so a stale case is
// noticed and removed, the way a stale importer workaround is.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
	"github.com/katbyte/sonarr-mcp/lib/client"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

// sweepCase is how the sweep treats one operation.
type sweepCase struct {
	// Skip leaves the operation uncalled, for the reason given.
	Skip string
	// Status is the error status Sonarr answers with, and Why says why that
	// is Sonarr's behaviour rather than a bug to fix.
	Status int
	Why    string
	// Path and Options supply arguments by parameter name and options field,
	// beyond what the fixtures resolve. Sonarr's document marks no option
	// required, so an operation that needs one (a series, a path, a search
	// term) gets it here.
	Path    map[string]string
	Options map[string]any
}

// sweepFixtures resolves path parameters from the suite's fixtures. A
// parameter is looked up by the literal path segments before its placeholder
// and its name, most specific first ("config/indexer/id", then "indexer/id"),
// then by the name alone, so /config/indexer/{id} and /indexer/{id} can
// resolve to different things.
type sweepFixtures map[string]string

func (f sweepFixtures) resolvePath(path, param string) (string, bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segments {
		if seg != "{"+param+"}" {
			continue
		}
		for from := max(i-2, 0); from < i; from++ {
			prefix := segments[from:i]
			if slices.ContainsFunc(prefix, func(s string) bool { return strings.Contains(s, "{") }) {
				continue
			}
			if v, ok := f[strings.Join(append(slices.Clone(prefix), param), "/")]; ok {
				return v, true
			}
		}
	}
	v, ok := f[param]

	return v, ok
}

// sweep calls every GET operation in the definitions.
func sweep(t *testing.T, fixtures sweepFixtures, cases map[string]sweepCase) {
	t.Helper()

	svc, err := definitions.Load(filepath.Join("..", "api-definitions", "sonarr"))
	if err != nil {
		t.Fatal(err)
	}

	gets := map[string]bool{}
	for _, op := range svc.Operations() {
		if op.Method != http.MethodGet {
			continue
		}
		gets[op.Name] = true
		c := cases[op.Name]

		t.Run(op.Name, func(t *testing.T) {
			if c.Skip != "" {
				t.Skip(c.Skip)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()

			args, err := sweepArgs(ctx, op, fixtures, c)
			if err != nil {
				t.Fatalf("%s: %v; resolve it in the fixtures or give it a sweepCase", op.Key(), err)
			}
			status, decodeErr, err := sweepCall(op, args)
			switch {
			case err == nil && c.Status != 0:
				t.Errorf("%s now answers; drop its sweepCase (%s)", op.Key(), c.Why)
			case err == nil:
			case c.Status != 0 && status == c.Status:
				t.Logf("%s: HTTP %d, as expected: %s", op.Key(), status, c.Why)
			case decodeErr:
				t.Errorf("%s answers what its model cannot decode: %v", op.Key(), err)
			default:
				t.Errorf("%s: %v", op.Key(), err)
			}
		})
	}

	for name := range cases {
		if !gets[name] {
			t.Errorf("the sweepCase for %s names no GET operation", name)
		}
	}
}

// sweepArgs builds the call: the context, the path parameters, and an options
// struct with the case's options set.
func sweepArgs(ctx context.Context, op *definitions.Operation, fixtures sweepFixtures, c sweepCase) ([]reflect.Value, error) {
	method := reflect.ValueOf(sc).MethodByName(op.Name)
	if !method.IsValid() {
		return nil, errors.New("the SDK has no such method")
	}
	mt := method.Type()
	args := []reflect.Value{reflect.ValueOf(ctx)}

	for _, p := range op.PathParameters {
		value, ok := c.Path[p.Name]
		if !ok {
			value, ok = fixtures.resolvePath(op.Path, p.Name)
		}
		if !ok {
			return nil, fmt.Errorf("no value for path parameter {%s}", p.Name)
		}
		v := reflect.New(mt.In(len(args))).Elem()
		if err := setValue(v, value); err != nil {
			return nil, fmt.Errorf("{%s}: %w", p.Name, err)
		}
		args = append(args, v)
	}
	if op.Request != nil {
		return nil, errors.New("a GET with a request body")
	}

	if len(op.Options) > 0 {
		options := reflect.New(mt.In(len(args))).Elem()
		for field, value := range c.Options {
			if err := setValue(options.FieldByName(field), value); err != nil {
				return nil, fmt.Errorf("option %s: %w", field, err)
			}
		}
		args = append(args, options)
	}
	if len(args) != mt.NumIn() {
		return nil, fmt.Errorf("built %d arguments for a method that takes %d", len(args), mt.NumIn())
	}

	return args, nil
}

// setValue sets a path argument or options field from a fixture: a string, a
// bool, a number, or a list.
func setValue(v reflect.Value, value any) error {
	if !v.IsValid() {
		return errors.New("no such field")
	}
	s := fmt.Sprint(value)
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Pointer:
		b, err := strconv.ParseBool(s)
		if err != nil || v.Type().Elem().Kind() != reflect.Bool {
			return fmt.Errorf("cannot set %s from %q", v.Type(), s)
		}
		v.Set(reflect.ValueOf(&b))
	case reflect.Slice:
		parts := strings.Split(s, ",")
		list := reflect.MakeSlice(v.Type(), len(parts), len(parts))
		for i, part := range parts {
			if err := setValue(list.Index(i), part); err != nil {
				return err
			}
		}
		v.Set(list)
	default:
		return fmt.Errorf("cannot set a %s", v.Type())
	}

	return nil
}

// sweepCall calls the operation, reads a streamed file, and classifies a
// failure: the status of a *client.StatusError, or a decode error (Sonarr
// answered in a documented status, but not the documented shape).
func sweepCall(op *definitions.Operation, args []reflect.Value) (status int, decodeErr bool, err error) {
	results := reflect.ValueOf(sc).MethodByName(op.Name).Call(args)
	resp, ok := reflect.TypeAssert[*http.Response](results[0].FieldByName("HttpResponse"))
	if !ok {
		return 0, false, errors.New("the result has no HttpResponse")
	}
	if !results[1].IsNil() {
		if err, ok = reflect.TypeAssert[error](results[1]); !ok {
			return 0, false, errors.New("the second result is not an error")
		}
	}

	if err == nil && resp != nil && op.Response != nil && op.Response.Type.Type == definitions.RawFile {
		defer func() { _ = resp.Body.Close() }()
		n, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		switch {
		case readErr != nil:
			return 0, false, fmt.Errorf("reading the file: %w", readErr)
		case n == 0:
			return 0, false, errors.New("the file is empty")
		}
	}
	if err == nil {
		return 0, false, nil
	}
	if status = client.StatusCode(err); status != 0 {
		return status, false, err
	}

	return 0, resp != nil, err
}

// TestReadSweep calls every GET against the fixtures, with one of each thing
// Sonarr lists by id made for it.
//
//nolint:paralleltest // the tests share one Sonarr
func TestReadSweep(t *testing.T) {
	ctx := skipUnlessUp(t)

	ff := seriesIDs[firefly.Title]
	e1, e6, e7 := episode(ctx, t, ff, 1), episode(ctx, t, ff, 6), episode(ctx, t, ff, 7)
	tasks := must(sc.GetSystemTask(ctx)).Model
	logs := must(sc.GetLogFile(ctx)).Model
	metadata := must(sc.GetMetadata(ctx)).Model
	if len(tasks) == 0 || len(logs) == 0 || len(metadata) == 0 || e1.EpisodeFileId == 0 {
		t.Fatalf("the fixtures are incomplete: %d tasks, %d log files, %d metadata consumers, S01E01's file %d",
			len(tasks), len(logs), len(metadata), e1.EpisodeFileId)
	}
	command := runCommand(ctx, t, "RefreshMonitoredDownloads", nil)
	made := sweepResources(ctx, t)

	id := strconv.Itoa
	fixtures := sweepFixtures{
		"autotagging/id":            id(made.autoTagging),
		"calendar/id":               id(e1.Id),
		"command/id":                id(command.Id),
		"customfilter/id":           id(made.customFilter),
		"customformat/id":           id(made.customFormat),
		"cutoff/id":                 id(e6.Id),
		"delayprofile/id":           "1", // the default profile, which cannot be deleted
		"detail/id":                 id(made.tag),
		"downloadclient/id":         id(downloadClient),
		"episode/id":                id(e1.Id),
		"episodefile/id":            id(e1.EpisodeFileId),
		"file/filename":             logs[0].Filename,
		"importlist/id":             id(made.importList),
		"importlistexclusion/id":    id(made.exclusion),
		"indexer/id":                id(indexerID),
		"language/id":               "1", // English
		"languageprofile/id":        "1", // the one fixed profile
		"localization/id":           "1", // any id answers the dictionary
		"mediacover/seriesId":       id(ff),
		"filename":                  "poster.jpg",
		"metadata/id":               id(metadata[0].Id),
		"missing/id":                id(e7.Id),
		"notification/id":           id(made.notification),
		"qualitydefinition/id":      "1",
		"qualityprofile/id":         id(profileID),
		"releaseprofile/id":         id(made.releaseProfile),
		"remotepathmapping/id":      id(made.remotePathMapping),
		"rootfolder/id":             id(rootFolderID),
		"series/id":                 id(ff),
		"tag/id":                    id(made.tag),
		"task/id":                   id(tasks[0].Id),
		"update/filename":           logs[0].Filename,
		"config/downloadclient/id":  "1", // each settings section is one resource, id 1
		"config/host/id":            "1",
		"config/importlist/id":      "1",
		"config/indexer/id":         "1",
		"config/mediamanagement/id": "1",
		"config/naming/id":          "1",
		"config/ui/id":              "1",
	}

	naming := must(sc.GetConfigNaming(ctx)).Model
	cases := map[string]sweepCase{
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
		"GetEpisode":              {Options: map[string]any{"SeriesId": ff}},
		"GetEpisodeFile":          {Options: map[string]any{"SeriesId": ff}},
		"GetFileSystem":           {Options: map[string]any{"Path": "/tv/", "IncludeFiles": true}},
		"GetFileSystemMediaFiles": {Options: map[string]any{"Path": "/tv/" + firefly.Folder}},
		"GetFileSystemType":       {Options: map[string]any{"Path": "/tv"}},
		"GetHistorySeries":        {Options: map[string]any{"SeriesId": ff}},
		"GetHistorySince":         {Options: map[string]any{"Date": "2000-01-01T00:00:00Z"}},
		"GetManualImport":         {Options: map[string]any{"SeriesId": ff}},
		"GetParse":                {Options: map[string]any{"Title": "Firefly.S01E07.Safe.720p.HDTV.x264-SDK"}},
		// without an episode or a season, the release list is every indexer's
		// RSS feed; an episode is the interactive search the tools use
		"GetRelease":      {Options: map[string]any{"EpisodeId": e7.Id}},
		"GetRename":       {Options: map[string]any{"SeriesId": ff}},
		"GetSeriesLookup": {Options: map[string]any{"Term": "tvdb:" + id(firefly.TvdbID)}},
		"GetLogFileUpdateByFilename": {
			Status: http.StatusNotFound,
			Why:    "a container is updated by replacing its image, so Sonarr never runs its updater and has no update log to read",
		},
	}

	sweep(t, fixtures, cases)
}

// sweepMade is what the sweep makes to read back by id.
type sweepMade struct {
	tag, customFormat, customFilter, autoTagging, releaseProfile, remotePathMapping, exclusion, importList, notification int
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

	x265 := formatSpec(t, must(sc.GetCustomFormatSchema(ctx)).Model, "ReleaseTitleSpecification", "x265", `\bx265\b`)
	m.customFormat = must(sc.PostCustomFormat(ctx, sonarr.CustomFormatResource{
		Name: "SDK Sweep", IncludeCustomFormatWhenRenaming: new(false), Specifications: []sonarr.CustomFormatSpecificationSchema{x265},
	})).Model.Id
	cleanup(func(ctx context.Context) error { _, err := sc.DeleteCustomFormatById(ctx, m.customFormat); return err })

	m.customFilter = must(sc.PostCustomFilter(ctx, sonarr.CustomFilterResource{
		Type: "series", Label: "SDK Sweep", Filters: []map[string]any{{"key": "monitored", "value": []any{true}, "type": "equal"}},
	})).Model.Id
	cleanup(func(ctx context.Context) error { _, err := sc.DeleteCustomFilterById(ctx, m.customFilter); return err })

	genre := tagSpec(t, must(sc.GetAutoTaggingSchema(ctx)).Model, "GenreSpecification", "Anime", []string{"Anime"})
	m.autoTagging = must(sc.PostAutoTagging(ctx, sonarr.AutoTaggingResource{
		Name: "SDK Sweep", RemoveTagsAutomatically: new(false), Tags: []int{m.tag}, Specifications: []sonarr.AutoTaggingSpecificationSchema{genre},
	})).Model.Id
	cleanup(func(ctx context.Context) error { _, err := sc.DeleteAutoTaggingById(ctx, m.autoTagging); return err })

	m.releaseProfile = must(sc.PostReleaseProfile(ctx, sonarr.ReleaseProfileResource{
		Name: "SDK Sweep", Enabled: new(true), Required: []string{"x265"}, Tags: []int{m.tag},
	})).Model.Id
	cleanup(func(ctx context.Context) error {
		_, err := sc.DeleteReleaseProfileById(ctx, m.releaseProfile)
		return err
	})

	m.remotePathMapping = must(sc.PostRemotePathMapping(ctx, sonarr.RemotePathMappingResource{
		Host: "sdk-sweep", RemotePath: "/remote/", LocalPath: "/downloads/",
	})).Model.Id
	cleanup(func(ctx context.Context) error {
		_, err := sc.DeleteRemotePathMappingById(ctx, m.remotePathMapping)
		return err
	})

	m.exclusion = must(sc.PostImportListExclusion(ctx, sonarr.ImportListExclusionResource{TvdbId: 1, Title: "SDK Sweep"})).Model.Id
	cleanup(func(ctx context.Context) error {
		_, err := sc.DeleteImportListExclusionById(ctx, m.exclusion)
		return err
	})

	m.importList = newImportList(ctx, t, "SDK Sweep").Id

	// a notification with no events is not tested when it is saved, so it
	// can point anywhere
	schemas := must(sc.GetNotificationSchema(ctx)).Model
	i := slices.IndexFunc(schemas, func(s sonarr.NotificationResource) bool { return s.Implementation == "Webhook" })
	if i < 0 {
		t.Fatal("sonarr has no Webhook notification schema")
	}
	n := schemas[i]
	n.Name, n.Fields = "SDK Sweep", fields(n.Fields, map[string]any{"url": "http://localhost:9/sweep", "method": 1})
	m.notification = must(sc.PostNotification(ctx, n, sonarr.PostNotificationOperationOptions{})).Model.Id
	cleanup(func(ctx context.Context) error { _, err := sc.DeleteNotificationById(ctx, m.notification); return err })

	return m
}
