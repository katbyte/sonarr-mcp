package importer

import (
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/config"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/openapi"
)

// miniSpec exercises one of every shape the importer normalises.
const miniSpec = `{
  "openapi": "3.0.1",
  "info": {"title": "Mini", "version": "1"},
  "paths": {
    "/Items": {"get": {"operationId": "GetItems", "tags": ["ItemsService"], "summary": "Gets items.", "parameters": [
        {"name": "userId", "in": "query", "schema": {"type": "string", "format": "uuid"}},
        {"name": "page", "in": "query", "schema": {"type": "integer", "format": "int32"}},
        {"name": "pageSize", "in": "query", "schema": {"type": "integer", "format": "int32"}},
        {"name": "ticks", "in": "query", "schema": {"type": "integer", "format": "int64"}},
        {"name": "rating", "in": "query", "schema": {"type": "number", "format": "double"}},
        {"name": "isFavorite", "in": "query", "schema": {"type": "boolean"}},
        {"name": "kind", "in": "query", "schema": {"allOf": [{"$ref": "#/components/schemas/Kind"}]}},
        {"name": "kinds", "in": "query", "schema": {"type": "array", "items": {"$ref": "#/components/schemas/Kind"}}},
        {"name": "ids", "in": "query", "style": "form", "explode": false, "schema": {"type": "array", "items": {"type": "string"}}},
        {"name": "old", "in": "query", "deprecated": true, "schema": {"type": "string"}},
        {"name": "X-Client", "in": "header", "schema": {"type": "string"}},
        {"name": "streamOptions", "in": "query", "schema": {"type": "object", "additionalProperties": {"type": "string"}}}],
      "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Query_Result.Item"}}}}}}},
    "/Items/{itemId}": {
      "get": {"operationId": "GetItem", "tags": ["ItemsService"], "parameters": [{"name": "itemId", "in": "path", "schema": {"type": "string"}}],
        "responses": {"200": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Item"}}, "application/xml": {}}}}},
      "post": {"operationId": "UpdateItem", "tags": ["ItemsService"], "parameters": [{"name": "itemId", "in": "path", "schema": {"type": "string"}}],
        "requestBody": {"content": {"application/*+json": {"schema": {"$ref": "#/components/schemas/Item"}}, "application/json": {"schema": {"$ref": "#/components/schemas/Item"}}}},
        "responses": {"204": {"description": "ok"}}}},
    "/Items/{itemId}/Images/{imageType}/{index}": {
      "get": {"operationId": "GetItemImage", "tags": ["ImageService"], "parameters": [
          {"name": "itemId", "in": "path", "schema": {"type": "string"}},
          {"name": "imageType", "in": "path", "schema": {"allOf": [{"$ref": "#/components/schemas/Kind"}]}},
          {"name": "index", "in": "path", "schema": {"type": "integer"}}],
        "responses": {"200": {"content": {"image/*": {"schema": {"type": "string", "format": "binary"}}}}}},
      "post": {"operationId": "SetItemImage", "tags": ["ImageService"], "parameters": [
          {"name": "itemId", "in": "path", "schema": {"type": "string"}},
          {"name": "imageType", "in": "path", "schema": {"allOf": [{"$ref": "#/components/schemas/Kind"}]}},
          {"name": "index", "in": "path", "schema": {"type": "integer"}}],
        "requestBody": {"content": {"image/*": {"schema": {"type": "string", "format": "binary"}}}},
        "responses": {"200": {"description": "ok"}, "204": {"description": "ok"}}}},
    "/Config/{key}": {"get": {"operationId": "GetConfig", "tags": ["ConfigService"], "parameters": [{"name": "key", "in": "path", "schema": {"type": "string"}}],
        "responses": {"200": {"content": {"application/json": {"schema": {"type": "string", "format": "binary"}}}}}}},
    "/Union": {"post": {"operationId": "PostUnion", "tags": ["ConfigService"],
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Union"}}}},
        "responses": {"200": {"content": {"application/json": {"schema": {"type": "array", "items": {"type": "string"}}}}}}}}
  },
  "components": {"schemas": {
    "Kind": {"type": "string", "enum": ["Movie", "Series"]},
    "Union": {"type": "object", "oneOf": [{"$ref": "#/components/schemas/Item"}]},
    "Unused": {"type": "object", "properties": {"Name": {"type": "string"}}},
    "Item": {"type": "object", "properties": {
      "Id": {"type": "string"}, "IsFolder": {"type": "boolean", "nullable": true}, "Parent": {"$ref": "#/components/schemas/Item"},
      "Kind": {"allOf": [{"$ref": "#/components/schemas/Kind"}]}, "Children": {"type": "array", "items": {"$ref": "#/components/schemas/Item"}},
      "Blur": {"type": "object", "properties": {"Primary": {"type": "object", "additionalProperties": {"type": "string"}}}},
      "Rating": {"type": "number", "format": "float"}}},
    "Query_Result.Item": {"type": "object", "properties": {"records": {"type": "array", "items": {"$ref": "#/components/schemas/Item"}}, "totalRecords": {"type": "integer"}}}
  }}
}`

var miniConfig = config.Service{Name: "mini", Package: "mini", Naming: config.OperationIDNaming, TagSuffix: "Service", Auth: "Sonarr"}

func importMini(t *testing.T, patch func(*openapi.Spec)) (*definitions.Service, error) {
	t.Helper()

	spec, err := openapi.Parse([]byte(miniSpec))
	if err != nil {
		t.Fatal(err)
	}
	if patch != nil {
		patch(spec)
	}

	return FromSpec(miniConfig, spec, nil, func(msg string) { t.Log(msg) })
}

func find[T any](items []T, match func(T) bool) *T {
	i := slices.IndexFunc(items, match)
	if i < 0 {
		return nil
	}

	return &items[i]
}

func TestImport(t *testing.T) {
	t.Parallel()

	svc, err := importMini(t, nil)
	if err != nil {
		t.Fatal(err)
	}

	groups := make([]string, 0, len(svc.Groups))
	for _, g := range svc.Groups {
		groups = append(groups, g.Name)
	}
	if !slices.Equal(groups, []string{"Common", "Config", "Image", "Items"}) {
		t.Fatalf("groups = %v", groups)
	}

	op := func(name string) *definitions.Operation {
		t.Helper()
		o := find(svc.Operations(), func(o *definitions.Operation) bool { return o.Name == name })
		if o == nil {
			t.Fatalf("no operation %s", name)
		}
		return *o
	}

	t.Run("options", func(t *testing.T) {
		t.Parallel()

		o := op("GetItems")
		want := map[string]string{
			"userId": "String", "pageSize": "Integer", "ticks": "Integer64", "rating": "Double", "isFavorite": "Boolean",
			"kind": "Reference(Kind)", "kinds": "List[Reference(Kind)]", "ids": "List[String]", "streamOptions": "Dictionary[String]", "X-Client": "String",
		}
		for name, typ := range want {
			opt := find(o.Options, func(opt definitions.Option) bool { return opt.Name == name })
			if opt == nil || opt.Type.String() != typ {
				t.Errorf("option %s = %+v, want %s", name, opt, typ)
			}
		}
		if ids := find(o.Options, func(opt definitions.Option) bool { return opt.Name == "ids" }); !ids.CommaSeparated {
			t.Error("ids declares explode false but is not comma-separated")
		}
		if kinds := find(o.Options, func(opt definitions.Option) bool { return opt.Name == "kinds" }); kinds.CommaSeparated {
			t.Error("kinds takes OpenAPI's default (one key per value) but is comma-separated")
		}
		if old := find(o.Options, func(opt definitions.Option) bool { return opt.Name == "old" }); !old.Deprecated {
			t.Error("the deprecated option is not marked")
		}
		if h := find(o.Options, func(opt definitions.Option) bool { return opt.Name == "X-Client" }); h.In != definitions.InHeader || h.Field != "XClient" {
			t.Errorf("header option = %+v", h)
		}
		if o.Pageable == nil || o.Pageable.PageOption != "Page" || o.Pageable.PageSizeOption != "PageSize" ||
			o.Pageable.RecordsField != "Records" || o.Pageable.TotalField != "TotalRecords" || o.Pageable.ItemType.String() != "Reference(Item)" {
			t.Errorf("Pageable = %+v", o.Pageable)
		}
		if o.Description != "Gets items." || !slices.Equal(o.ExpectedStatusCodes, []int{200}) {
			t.Errorf("GetItems = %+v", o)
		}
	})

	t.Run("bodies", func(t *testing.T) {
		t.Parallel()

		if o := op("GetItem"); o.Response.ContentType != "application/json" || o.Response.Type.String() != "Reference(Item)" || o.Pageable != nil {
			t.Errorf("GetItem response = %+v (xml twin ignored)", o.Response)
		}
		if o := op("UpdateItem"); o.Request.ContentType != "application/json" || o.Request.Type.String() != "Reference(Item)" || o.Response != nil ||
			!slices.Equal(o.ExpectedStatusCodes, []int{204}) {
			t.Errorf("UpdateItem = %+v", o)
		}
		img := op("GetItemImage")
		if img.Response.Type.Type != definitions.RawFile || img.Response.ContentType != "image/*" {
			t.Errorf("GetItemImage response = %+v", img.Response)
		}
		args := make([]string, 0, len(img.PathParameters))
		for _, p := range img.PathParameters {
			args = append(args, p.Argument+" "+p.Type.String())
		}
		if !slices.Equal(args, []string{"itemId String", "imageType Reference(Kind)", "index Integer"}) {
			t.Errorf("path parameters = %v", args)
		}
		if o := op("SetItemImage"); o.Request.Type.Type != definitions.RawFile || o.Request.ContentType != "image/*" || !slices.Equal(o.ExpectedStatusCodes, []int{200, 204}) {
			t.Errorf("SetItemImage = %+v", o)
		}
		// a binary string answered as JSON is a JSON document of no schema
		if o := op("GetConfig"); o.Response.Type.Type != definitions.RawObject {
			t.Errorf("GetConfig response = %+v", o.Response)
		}
		if o := op("PostUnion"); o.Request.Type.String() != "Reference(Union)" || o.Response.Type.String() != "List[String]" {
			t.Errorf("PostUnion = %+v", o)
		}
	})

	t.Run("models", func(t *testing.T) {
		t.Parallel()

		models := svc.Models()
		item := models["Item"]
		if item == nil {
			t.Fatal("no Item model")
		}
		fields := map[string]string{}
		for _, f := range item.Fields {
			fields[f.JSONName] = f.Type.String()
		}
		want := map[string]string{
			"Id": "String", "IsFolder": "Boolean", "Parent": "Reference(Item)", "Kind": "Reference(Kind)",
			"Children": "List[Reference(Item)]", "Blur": "Reference(ItemBlur)", "Rating": "Float",
		}
		for k, v := range want {
			if fields[k] != v {
				t.Errorf("Item.%s = %q, want %q", k, fields[k], v)
			}
		}
		if f := find(item.Fields, func(f definitions.Field) bool { return f.Name == "IsFolder" }); !f.Nullable {
			t.Error("IsFolder lost its nullable flag")
		}
		if blur := models["ItemBlur"]; blur == nil || blur.SchemaName != "" || blur.Fields[0].Type.String() != "Dictionary[String]" {
			t.Errorf("inline model = %+v", blur)
		}
		if u := models["Union"]; u == nil || !slices.Equal(u.Union, []string{"Item"}) {
			t.Errorf("union = %+v", u)
		}
		if qr := models["QueryResultItem"]; qr == nil || qr.SchemaName != "Query_Result.Item" {
			t.Errorf("QueryResultItem = %+v", qr)
		}
		if k := svc.Constants()["Kind"]; k == nil || len(k.Values) != 2 || k.Values[1] != (definitions.ConstantValue{Name: "KindSeries", Value: "Series"}) {
			t.Errorf("Kind = %+v", k)
		}
	})

	t.Run("ownership", func(t *testing.T) {
		t.Parallel()

		owner := map[string]string{}
		for _, g := range svc.Groups {
			for _, m := range g.Models {
				owner[m.Name] = g.Name
			}
			for _, c := range g.Constants {
				owner[c.Name] = g.Name
			}
		}
		// Kind is used by Items and Image; Item only by Items (a union names its
		// variants but does not use them)
		want := map[string]string{"QueryResultItem": "Items", "Item": "Items", "ItemBlur": "Items", "Kind": "Common", "Union": "Config", "Unused": "Common"}
		for name, g := range want {
			if owner[name] != g {
				t.Errorf("%s is in %s, want %s", name, owner[name], g)
			}
		}
	})
}

func TestPathNaming(t *testing.T) {
	t.Parallel()

	spec, err := openapi.Parse([]byte(miniSpec))
	if err != nil {
		t.Fatal(err)
	}
	cfg := miniConfig
	cfg.Naming = config.PathNaming
	svc, err := FromSpec(cfg, spec, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(svc.Operations()))
	for _, o := range svc.Operations() {
		names = append(names, o.Name)
	}
	slices.Sort(names)
	want := []string{"GetConfigByKey", "GetItems", "GetItemsByItemId", "GetItemsByItemIdImagesByImageTypeByIndex", "PostItemsByItemId", "PostItemsByItemIdImagesByImageTypeByIndex", "PostUnion"}
	if !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// The importer refuses what it cannot normalise faithfully, listing every
// problem, so each gets a workaround rather than a silent guess.
func TestImportFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		patch func(*openapi.Spec)
		want  string
	}{
		{
			name:  "undeclared path parameter",
			patch: func(s *openapi.Spec) { s.Operation("GET", "/Items/{itemId}").Parameters = nil },
			want:  "GET /Items/{itemId}: path parameter {itemId} is not declared",
		},
		{
			name: "declared parameter the template lacks",
			patch: func(s *openapi.Spec) {
				op := s.Operation("GET", "/Items")
				op.Parameters = append(op.Parameters, &openapi.Parameter{Name: "id", In: openapi.InPath})
			},
			want: `GET /Items: declares path parameter "id" the template does not have`,
		},
		{
			name:  "GET with no response content",
			patch: func(s *openapi.Spec) { s.Operation("GET", "/Items/{itemId}").Responses["200"].Content = nil },
			want:  "GET /Items/{itemId}: declares no response content",
		},
		{
			name:  "duplicate operationId",
			patch: func(s *openapi.Spec) { s.Operation("POST", "/Items/{itemId}").OperationID = "GetItem" },
			want:  `operationId "GetItem" is also`,
		},
		{
			name: "no success response",
			patch: func(s *openapi.Spec) {
				s.Operation("POST", "/Items/{itemId}").Responses = map[string]*openapi.Response{"404": {}}
			},
			want: "POST /Items/{itemId}: declares no success response",
		},
		{
			name:  "no tag",
			patch: func(s *openapi.Spec) { s.Operation("POST", "/Union").Tags = nil },
			want:  "POST /Union: has no tag",
		},
		{
			name:  "type name clash",
			patch: func(s *openapi.Spec) { s.Components.Schemas["ItemBlur"] = &openapi.Schema{Type: "object"} },
			want:  "type ItemBlur is declared by both",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := importMini(t, tt.patch)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "fix each with a workaround") {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}
