package generator_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/config"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/generator"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/importer"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/openapi"
)

// miniSpec has one of every method shape the generator writes.
const miniSpec = `{
  "openapi": "3.0.1",
  "info": {"title": "Mini", "version": "1"},
  "paths": {
    "/Items": {"get": {"operationId": "GetItems", "tags": ["Items"], "summary": "Gets items. More words.", "parameters": [
        {"name": "userId", "in": "query", "schema": {"type": "string"}},
        {"name": "page", "in": "query", "schema": {"type": "integer"}},
        {"name": "pageSize", "in": "query", "schema": {"type": "integer"}},
        {"name": "ticks", "in": "query", "schema": {"type": "integer", "format": "int64"}},
        {"name": "rating", "in": "query", "schema": {"type": "number"}},
        {"name": "isFavorite", "in": "query", "schema": {"type": "boolean"}},
        {"name": "kind", "in": "query", "schema": {"$ref": "#/components/schemas/Kind"}},
        {"name": "kinds", "in": "query", "schema": {"type": "array", "items": {"$ref": "#/components/schemas/Kind"}}},
        {"name": "years", "in": "query", "schema": {"type": "array", "items": {"type": "integer"}}},
        {"name": "ids", "in": "query", "explode": false, "schema": {"type": "array", "items": {"type": "string"}}},
        {"name": "old", "in": "query", "deprecated": true, "schema": {"type": "string"}},
        {"name": "X-Client", "in": "header", "schema": {"type": "string"}},
        {"name": "streamOptions", "in": "query", "schema": {"type": "object", "additionalProperties": {"type": "string"}}}],
      "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/QueryResult"}}}}}}},
    "/Items/{itemId}": {
      "get": {"operationId": "GetItem", "tags": ["Items"], "parameters": [{"name": "itemId", "in": "path", "schema": {"type": "string"}}],
        "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Item"}}}}}},
      "post": {"operationId": "UpdateItem", "tags": ["Items"], "parameters": [{"name": "itemId", "in": "path", "schema": {"type": "string"}}],
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Item"}}}},
        "responses": {"204": {"description": "ok"}}},
      "delete": {"operationId": "DeleteItem", "tags": ["Items"], "deprecated": true, "parameters": [{"name": "itemId", "in": "path", "schema": {"type": "string"}}],
        "responses": {"204": {"description": "ok"}}}},
    "/Items/{itemId}/Images/{imageType}/{index}": {
      "get": {"operationId": "GetItemImage", "tags": ["Image"], "parameters": [
          {"name": "itemId", "in": "path", "schema": {"type": "string"}},
          {"name": "imageType", "in": "path", "schema": {"$ref": "#/components/schemas/Kind"}},
          {"name": "index", "in": "path", "schema": {"type": "integer"}}],
        "responses": {"200": {"content": {"image/*": {"schema": {"type": "string", "format": "binary"}}}}}},
      "post": {"operationId": "SetItemImage", "tags": ["Image"], "parameters": [
          {"name": "itemId", "in": "path", "schema": {"type": "string"}},
          {"name": "imageType", "in": "path", "schema": {"$ref": "#/components/schemas/Kind"}},
          {"name": "index", "in": "path", "schema": {"type": "integer"}}],
        "requestBody": {"content": {"image/*": {"schema": {"type": "string", "format": "binary"}}}},
        "responses": {"204": {"description": "ok"}}}},
    "/Sync/{jobId}/{ratio}": {"put": {"operationId": "PutSync", "tags": ["Sync"], "parameters": [
          {"name": "jobId", "in": "path", "schema": {"type": "integer", "format": "int64"}},
          {"name": "ratio", "in": "path", "schema": {"type": "number"}}],
        "requestBody": {"content": {"application/json": {"schema": {"type": "array", "items": {"$ref": "#/components/schemas/Item"}}}}},
        "responses": {"200": {"content": {"application/json": {"schema": {"type": "array", "items": {"type": "string"}}}}}}}},
    "/Keys": {"get": {"operationId": "GetKeys", "tags": ["Sync"], "responses": {"200": {"content": {"application/json": {}}}}}},
    "/Name": {"get": {"operationId": "GetName", "tags": ["Sync"], "responses": {"200": {"content": {"application/json": {"schema": {"type": "string"}}}}}}},
    "/Kinds": {"get": {"operationId": "GetKind", "tags": ["Sync"], "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Kind"}}}}}}},
    "/Union": {"post": {"operationId": "PostUnion", "tags": ["Sync"],
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Union"}}}},
        "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Union"}}}}}}},
    "/Linux": {"get": {"operationId": "GetLinux", "tags": ["Sync"], "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Linux"}}}}}}}
  },
  "components": {"schemas": {
    "Kind": {"type": "string", "enum": ["Movie", "Series"]},
    "Union": {"type": "object", "oneOf": [{"$ref": "#/components/schemas/Item"}]},
    "Linux": {"type": "object"},
    "Item": {"type": "object", "properties": {
      "Id": {"type": "string"}, "IsFolder": {"type": "boolean"}, "Parent": {"$ref": "#/components/schemas/Item"},
      "Kind": {"$ref": "#/components/schemas/Kind"}, "Children": {"type": "array", "items": {"$ref": "#/components/schemas/Item"}},
      "Blur": {"type": "object", "properties": {"Primary": {"type": "object", "additionalProperties": {"type": "string"}}}},
      "Rating": {"type": "number", "format": "float"}}},
    "QueryResult": {"type": "object", "properties": {"records": {"type": "array", "items": {"$ref": "#/components/schemas/Item"}}, "totalRecords": {"type": "integer"}}}
  }}
}`

func miniDefinitions(t *testing.T) *definitions.Service {
	t.Helper()

	spec, err := openapi.Parse([]byte(miniSpec))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := importer.FromSpec(config.Service{Name: "mini", Package: "mini", Naming: config.OperationIDNaming, Auth: "Sonarr"}, spec, []string{"mini-fix"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	return svc
}

// generateInModule writes the package under testdata in this directory, so
// it builds against the module's lib/client, and removes it afterwards.
func generateInModule(t *testing.T, svc *definitions.Service) string {
	t.Helper()

	dir := filepath.Join("testdata", "mini")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll("testdata") })
	if _, err := generator.Generate(svc, generator.Options{Dir: dir, Definitions: "api-definitions/mini"}); err != nil {
		t.Fatal(err)
	}

	return dir
}

func read(t *testing.T, dir, name string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // a generated test file
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

//nolint:paralleltest // writes the testdata package the other test would remove
func TestGenerate(t *testing.T) {
	dir := generateInModule(t, miniDefinitions(t))

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Name())
	}
	for _, want := range []string{
		"client.go", "doc.go", "common_constants.go", "common_model_item.go", "items_model_queryresult.go",
		"common_model_itemblur.go", "sync_model_union.go", "items_method_getitems.go", "image_method_setitemimage.go",
		// every operation has a generated test, and the tests share client_test.go
		"items_method_getitems_test.go", "image_method_setitemimage_test.go", "client_test.go",
		// a file named for a GOOS would only build on it
		"sync_model_linuxtype.go",
	} {
		if !slices.Contains(files, want) {
			t.Errorf("no %s among %v", want, files)
		}
	}

	t.Run("signatures", func(t *testing.T) {
		all := strings.Join(func() []string {
			out := make([]string, 0, len(files))
			for _, f := range files {
				out = append(out, read(t, dir, f))
			}
			return out
		}(), "\n")
		for _, want := range []string{
			"func (c Client) GetItems(ctx context.Context, options GetItemsOperationOptions) (result GetItemsOperationResponse, err error) {",
			"func (c Client) GetItemsComplete(ctx context.Context, options GetItemsOperationOptions) (result GetItemsCompleteResult, err error) {",
			"func (c Client) GetItem(ctx context.Context, itemId string) (result GetItemOperationResponse, err error) {",
			"func (c Client) UpdateItem(ctx context.Context, itemId string, input Item) (result UpdateItemOperationResponse, err error) {",
			"func (c Client) GetItemImage(ctx context.Context, itemId string, imageType Kind, index int) (result GetItemImageOperationResponse, err error) {",
			"func (c Client) SetItemImage(ctx context.Context, itemId string, imageType Kind, index int, input io.Reader, contentType string) (result SetItemImageOperationResponse, err error) {",
			"func (c Client) PutSync(ctx context.Context, jobId int64, ratio float64, input []Item) (result PutSyncOperationResponse, err error) {",
			`fmt.Sprintf("/Items/%s/Images/%s/%d", url.PathEscape(itemId), url.PathEscape(string(imageType)), index),`,
			`fmt.Sprintf("/Sync/%d/%s", jobId, strconv.FormatFloat(ratio, 'f', -1, 64))`,
			"StreamResponse: true,",
			"http.StatusNoContent,",
			"// Deprecated: the document marks this operation deprecated.",
			"// Deprecated: the document marks this parameter deprecated.",
			// response models: pointer for a struct, primitive or constant; value for the rest
			"Model        *QueryResult\n", "Model        []string\n", "Model        json.RawMessage\n",
			"Model        *string\n", "Model        *Kind\n", "Model        Union\n",
			// options on the wire
			`out.Append("isFavorite", strconv.FormatBool(*o.IsFavorite))`,
			`out.Append("ticks", strconv.FormatInt(o.Ticks, 10))`,
			`out.Append("kind", string(o.Kind))`,
			"for _, v := range o.Kinds {\n\t\tout.Append(\"kinds\", string(v))",
			"for _, v := range o.Years {\n\t\tout.Append(\"years\", strconv.Itoa(v))",
			`out.Append("ids", client.CSV(o.Ids))`,
			`out.Append("streamOptions", client.JSONObject(o.StreamOptions))`,
			`out.Append("X-Client", o.XClient)`,
			// model fields
			"IsFolder *bool", "Children []Item",
			"func PossibleValuesForKind() []string {",
			"type Union = json.RawMessage",
			"type Linux struct{}",
			"client.APIKey(apiKey)",
			"//   - mini-fix",
		} {
			if !strings.Contains(all, want) {
				t.Errorf("the package lacks %q", want)
			}
		}
		for _, re := range []string{"`json:\"IsFolder,omitempty\"`", "`json:\"Children,omitzero\"`", "`json:\"Parent,omitempty\"`", `Parent +\*Item`, `Blur +\*ItemBlur`, `Rating +float32`} {
			if !regexp.MustCompile(re).MatchString(all) {
				t.Errorf("the package lacks %s", re)
			}
		}
		if !strings.HasPrefix(read(t, dir, "items_method_getitems.go"), generator.Header) {
			t.Error("a generated file lacks the header")
		}
	})

	t.Run("builds", func(t *testing.T) {
		out, err := exec.CommandContext(t.Context(), "go", "vet", "./"+filepath.ToSlash(dir)).CombinedOutput() //nolint:gosec // a fixed command on the test's own package
		if err != nil {
			t.Fatalf("go vet on the generated package: %v\n%s", err, out)
		}
	})

	t.Run("check", func(t *testing.T) {
		svc := miniDefinitions(t)
		n, err := generator.Check(svc, dir)
		if err != nil || n != len(svc.Operations()) {
			t.Fatalf("Check = %d, %v", n, err)
		}
		path := filepath.Join(dir, "items_method_getitems.go")
		src := strings.Replace(read(t, dir, "items_method_getitems.go"), "func (c Client) GetItemsComplete(", "func (c Client) getItemsComplete(", 1)
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := generator.Check(svc, dir); err == nil || !strings.Contains(err.Error(), "GetItemsComplete (GET /Items)") {
			t.Errorf("Check with a method gone = %v", err)
		}
	})

	t.Run("stale files removed, hand-written kept", func(t *testing.T) {
		svc := miniDefinitions(t)
		stale := filepath.Join(dir, "sync_method_gone.go")
		kept := filepath.Join(dir, "extra_test.go")
		manual := filepath.Join(dir, "manual.go")
		for path, src := range map[string]string{stale: generator.Header + "\npackage mini\n", kept: "package mini\n", manual: "package mini\n"} {
			if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := generator.Generate(svc, generator.Options{Dir: dir}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Error("a stale generated file survived")
		}
		for _, path := range []string{kept, manual} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s was removed: %v", path, err)
			}
		}
	})
}

func TestRenderRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		patch func(*definitions.Service)
		want  string
	}{
		{"unknown auth", func(s *definitions.Service) { s.Auth = "Plex" }, "unknown Auth"},
		{"bad package", func(s *definitions.Service) { s.Package = "my-pkg" }, "is not a package name"},
		{"dangling reference", func(s *definitions.Service) {
			s.Groups[0].Models = slices.DeleteFunc(s.Groups[0].Models, func(m definitions.Model) bool { return m.Name == "Item" })
		}, `reference to undefined type "Item"`},
		{"identifier clash", func(s *definitions.Service) {
			for gi := range s.Groups {
				for oi := range s.Groups[gi].Operations {
					if s.Groups[gi].Operations[oi].Name == "GetKind" {
						s.Groups[gi].Operations[oi].Name = "GetItems" + "Complete"
					}
				}
			}
		}, "Client.GetItemsComplete is declared by both operation GetItems and operation GetItemsComplete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := miniDefinitions(t)
			tt.patch(svc)
			if _, err := generator.Render(svc, generator.Options{}); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Render = %v, want containing %q", err, tt.want)
			}
		})
	}
}
