// Package openapi decodes the subset of an OpenAPI 3 document the importer
// reads. Anything not listed here (servers, security, examples, xml hints) is
// ignored. The types are mutable on purpose: workarounds patch a loaded
// document before it is normalised into definitions.
package openapi

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Spec is an OpenAPI 3 document.
type Spec struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"info"`
	Paths      map[string]*PathItem `json:"paths"`
	Components struct {
		Schemas map[string]*Schema `json:"schemas"`
	} `json:"components"`
}

// PathItem holds the operations of one path. HEAD and OPTIONS are not
// imported: the servers use them only for cache probes and CORS.
type PathItem struct {
	Get    *Operation `json:"get"`
	Post   *Operation `json:"post"`
	Put    *Operation `json:"put"`
	Delete *Operation `json:"delete"`
	Patch  *Operation `json:"patch"`
}

// Operation is one HTTP method on one path.
type Operation struct {
	OperationID string               `json:"operationId"`
	Tags        []string             `json:"tags"`
	Summary     string               `json:"summary"`
	Description string               `json:"description"`
	Deprecated  bool                 `json:"deprecated"`
	Parameters  []*Parameter         `json:"parameters"`
	RequestBody *RequestBody         `json:"requestBody"`
	Responses   map[string]*Response `json:"responses"`
}

// Parameter is a path, query or header parameter.
type Parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description"`
	Required    bool    `json:"required"`
	Deprecated  bool    `json:"deprecated"`
	Style       string  `json:"style"`
	Explode     *bool   `json:"explode"`
	Schema      *Schema `json:"schema"`
}

// Exploded reports whether an array parameter is sent as one key per value,
// OpenAPI's default for query parameters (style form, explode true), rather
// than comma-separated.
func (p *Parameter) Exploded() bool {
	if p.Explode != nil {
		return *p.Explode
	}

	return p.Style == "" || p.Style == "form"
}

// Parameter locations.
const (
	InPath   = "path"
	InQuery  = "query"
	InHeader = "header"
)

// RequestBody maps media types to their schema.
type RequestBody struct {
	Required bool                  `json:"required"`
	Content  map[string]*MediaType `json:"content"`
}

// MediaType is the schema of one media type of a body or response.
type MediaType struct {
	Schema *Schema `json:"schema"`
}

// Response is one status code of an operation. Ref is set when the response
// is a `$ref` into components/responses (Emby does this for its error codes);
// those never carry content, so the reference is not followed.
type Response struct {
	Ref         string                `json:"$ref"`
	Description string                `json:"description"`
	Content     map[string]*MediaType `json:"content"`
}

// Schema is a JSON schema object, as far as the two specs use it.
type Schema struct {
	Ref                  string             `json:"$ref"`
	Type                 string             `json:"type"`
	Format               string             `json:"format"`
	Description          string             `json:"description"`
	Nullable             bool               `json:"nullable"`
	Enum                 []json.RawMessage  `json:"enum"`
	Properties           map[string]*Schema `json:"properties"`
	Items                *Schema            `json:"items"`
	AdditionalProperties json.RawMessage    `json:"additionalProperties"`
	AllOf                []*Schema          `json:"allOf"`
	OneOf                []*Schema          `json:"oneOf"`
	AnyOf                []*Schema          `json:"anyOf"`
}

// SchemaRefPrefix is how a `$ref` names a component schema.
const SchemaRefPrefix = "#/components/schemas/"

// JSON schema type names.
const (
	TypeString  = "string"
	TypeInteger = "integer"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
	TypeArray   = "array"
	TypeObject  = "object"
)

// Load reads and decodes an OpenAPI 3 JSON document.
func Load(path string) (*Spec, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the spec path comes from the service config
	if err != nil {
		return nil, err
	}

	return Parse(raw)
}

// Parse decodes an OpenAPI 3 JSON document.
func Parse(raw []byte) (*Spec, error) {
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decoding spec: %w", err)
	}
	if !strings.HasPrefix(s.OpenAPI, "3.") {
		return nil, fmt.Errorf("unsupported openapi version %q (want 3.x)", s.OpenAPI)
	}

	return &s, nil
}

// RefName returns the schema name a `$ref` points at, or "" when the schema
// is not a reference. A single-element allOf wrapping a $ref (how Jellyfin
// attaches descriptions and nullable to references) counts as a reference.
func (s *Schema) RefName() string {
	if s == nil {
		return ""
	}
	if s.Ref != "" {
		return strings.TrimPrefix(s.Ref, SchemaRefPrefix)
	}
	if len(s.AllOf) == 1 && s.AllOf[0].Ref != "" {
		return strings.TrimPrefix(s.AllOf[0].Ref, SchemaRefPrefix)
	}

	return ""
}

// Additional returns the additionalProperties schema. ok is false when the
// schema does not allow additional properties; a bare `true` or `{}` yields an
// empty schema.
func (s *Schema) Additional() (sch *Schema, ok bool) {
	raw := strings.TrimSpace(string(s.AdditionalProperties))
	switch raw {
	case "", "false":
		return nil, false
	case "true", "{}":
		return &Schema{}, true
	}
	var a Schema
	if err := json.Unmarshal(s.AdditionalProperties, &a); err != nil {
		return nil, false
	}

	return &a, true
}

// EnumValues returns the enum members as strings. Both specs only use string
// enums; anything else is rendered through its JSON form.
func (s *Schema) EnumValues() []string {
	out := make([]string, 0, len(s.Enum))
	for _, raw := range s.Enum {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			v = string(raw)
		}
		out = append(out, v)
	}

	return out
}

// IsEnum reports whether a schema defines a string enum type.
func (s *Schema) IsEnum() bool {
	return s != nil && len(s.Enum) > 0 && (s.Type == TypeString || s.Type == "")
}

// IsUnion reports whether a schema is a oneOf/anyOf union, which cannot be
// represented as a struct.
func (s *Schema) IsUnion() bool {
	return s != nil && (len(s.OneOf) > 0 || len(s.AnyOf) > 0)
}

// Method is one operation of a path together with its HTTP method.
type Method struct {
	Method    string // GET, POST, ...
	Operation *Operation
}

// Methods returns the imported operations of a path in a fixed method order.
func (p *PathItem) Methods() []Method {
	all := []Method{{"GET", p.Get}, {"POST", p.Post}, {"PUT", p.Put}, {"DELETE", p.Delete}, {"PATCH", p.Patch}}

	return slices.DeleteFunc(all, func(m Method) bool { return m.Operation == nil })
}

// Operation returns the operation for an HTTP method on a path, or nil.
func (s *Spec) Operation(method, path string) *Operation {
	item := s.Paths[path]
	if item == nil {
		return nil
	}
	for _, m := range item.Methods() {
		if strings.EqualFold(m.Method, method) {
			return m.Operation
		}
	}

	return nil
}

// SetOperation replaces (or with nil removes) the operation for an HTTP
// method on a path.
func (s *Spec) SetOperation(method, path string, op *Operation) {
	item := s.Paths[path]
	if item == nil {
		if op == nil {
			return
		}
		item = &PathItem{}
		if s.Paths == nil {
			s.Paths = map[string]*PathItem{}
		}
		s.Paths[path] = item
	}
	switch strings.ToUpper(method) {
	case "GET":
		item.Get = op
	case "POST":
		item.Post = op
	case "PUT":
		item.Put = op
	case "DELETE":
		item.Delete = op
	case "PATCH":
		item.Patch = op
	}
}

// Parameter returns the operation's parameter of that location and name
// (case-insensitive, as both servers bind them), or nil.
func (o *Operation) Parameter(in, name string) *Parameter {
	for _, p := range o.Parameters {
		if p.In == in && strings.EqualFold(p.Name, name) {
			return p
		}
	}

	return nil
}

// SortedKeys returns the keys of a map in sorted order, for deterministic output.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	return keys
}
