package importer

import (
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
	"github.com/katbyte/sonarr-mcp/internal/pandorest/openapi"
)

// schemaKind classifies a named component schema.
type schemaKind int

const (
	kindObject schemaKind = iota
	kindEnum
	kindUnion
	// kindAlias is a named scalar or array; references to it are replaced
	// by the type it names. Neither vendored document has one.
	kindAlias
)

func (im *importer) kindOf(schemaName string) schemaKind {
	s := im.spec.Components.Schemas[schemaName]
	switch {
	case s == nil:
		return kindObject
	case s.IsEnum():
		return kindEnum
	case s.IsUnion():
		return kindUnion
	case len(s.Properties) == 0 && len(s.AdditionalProperties) > 0 && string(s.AdditionalProperties) != "false":
		// a named map (Emby's ProviderIdDictionary): the map, not an empty struct
		return kindAlias
	case s.Type == openapi.TypeObject || s.Type == "" || len(s.Properties) > 0:
		return kindObject
	default:
		return kindAlias
	}
}

// typeRef maps a schema to a definition type. owner and field name the
// enclosing model and property so an inline object becomes a model of its own
// named <Owner><Field>.
func (im *importer) typeRef(s *openapi.Schema, owner, field string) definitions.TypeRef {
	if s == nil {
		return definitions.TypeRef{Type: definitions.Any}
	}
	if ref := s.RefName(); ref != "" {
		if _, ok := im.spec.Components.Schemas[ref]; !ok {
			im.fail(fmt.Sprintf("%s.%s: $ref to undefined schema %q", owner, field, ref))
			return definitions.TypeRef{Type: definitions.Any}
		}
		if im.kindOf(ref) == kindAlias {
			return im.typeRef(im.spec.Components.Schemas[ref], owner, field)
		}
		return reference(typeName(ref))
	}
	if s.IsUnion() || len(s.AllOf) > 1 {
		return definitions.TypeRef{Type: definitions.RawObject}
	}

	switch s.Type {
	case openapi.TypeString:
		return definitions.TypeRef{Type: definitions.String}
	case openapi.TypeInteger:
		if s.Format == "int64" {
			return definitions.TypeRef{Type: definitions.Integer64}
		}
		return definitions.TypeRef{Type: definitions.Integer}
	case openapi.TypeNumber:
		if s.Format == "float" {
			return definitions.TypeRef{Type: definitions.Float}
		}
		return definitions.TypeRef{Type: definitions.Double}
	case openapi.TypeBoolean:
		return definitions.TypeRef{Type: definitions.Boolean}
	case openapi.TypeArray:
		item := im.typeRef(s.Items, owner, field)
		return definitions.TypeRef{Type: definitions.List, NestedItem: &item}
	case openapi.TypeObject, "":
		if len(s.Properties) > 0 {
			name := owner + field
			im.addInlineModel(name, s)
			return reference(name)
		}
		if add, ok := s.Additional(); ok {
			item := im.typeRef(add, owner, field)
			return definitions.TypeRef{Type: definitions.Dictionary, NestedItem: &item}
		}
		if s.Type == openapi.TypeObject {
			return definitions.TypeRef{Type: definitions.Dictionary, NestedItem: &definitions.TypeRef{Type: definitions.Any}}
		}
		return definitions.TypeRef{Type: definitions.Any}
	default:
		im.warn(fmt.Sprintf("unknown schema type %q for %s.%s; using Any", s.Type, owner, field))
		return definitions.TypeRef{Type: definitions.Any}
	}
}

func reference(name string) definitions.TypeRef {
	return definitions.TypeRef{Type: definitions.Reference, ReferenceName: name}
}

// importSchemas turns every component schema into a model or constant.
func (im *importer) importSchemas() {
	for _, schemaName := range openapi.SortedKeys(im.spec.Components.Schemas) {
		s := im.spec.Components.Schemas[schemaName]
		name := typeName(schemaName)
		switch im.kindOf(schemaName) {
		case kindEnum:
			im.claimName(name, "schema "+schemaName)
			im.constants[name] = im.constant(name, schemaName, s)
		case kindUnion:
			im.claimName(name, "schema "+schemaName)
			im.models[name] = unionModel(name, schemaName, s)
		case kindAlias:
			// resolved where it is referenced
		default:
			im.claimName(name, "schema "+schemaName)
			m := im.objectModel(name, schemaName, s)
			m.WrittenWhole = slices.Contains(im.cfg.WrittenWhole, schemaName)
			im.models[name] = &m
		}
	}
	for _, schemaName := range im.cfg.WrittenWhole {
		if s := im.spec.Components.Schemas[schemaName]; s == nil || im.kindOf(schemaName) != kindObject {
			im.fail(fmt.Sprintf("the config names %s as written whole, but the document has no object schema by that name", schemaName))
		}
	}
}

// claimName records a Go type name, failing the import when two schemas (or
// a schema and an inline object) would declare the same type.
func (im *importer) claimName(name, origin string) {
	if prev, ok := im.typeNames[name]; ok {
		im.fail(fmt.Sprintf("type %s is declared by both %s and %s", name, prev, origin))
		return
	}
	im.typeNames[name] = origin
}

func (im *importer) addInlineModel(name string, s *openapi.Schema) {
	if _, ok := im.models[name]; ok {
		return
	}
	im.claimName(name, "an inline object")
	m := &definitions.Model{Name: name, Description: cleanText(s.Description)}
	im.models[name] = m // reserve first: the object may reference itself
	*m = im.objectModel(name, "", s)
}

// objectModel builds the fields of an object schema.
func (im *importer) objectModel(name, schemaName string, s *openapi.Schema) definitions.Model {
	m := definitions.Model{Name: name, SchemaName: schemaName, Description: cleanText(s.Description)}
	seen := map[string]string{}
	for _, prop := range openapi.SortedKeys(s.Properties) {
		ps := s.Properties[prop]
		fname := fieldName(prop)
		if prev, ok := seen[fname]; ok {
			im.warn(fmt.Sprintf("%s: properties %q and %q are both field %s; %q skipped", name, prev, prop, fname, prop))
			continue
		}
		seen[fname] = prop
		m.Fields = append(m.Fields, definitions.Field{
			Name:        fname,
			JSONName:    prop,
			Description: cleanText(ps.Description),
			Type:        im.typeRef(ps, name, fname),
			Nullable:    ps.Nullable,
		})
	}

	return m
}

// constant builds a string enum.
func (im *importer) constant(name, schemaName string, s *openapi.Schema) *definitions.Constant {
	c := &definitions.Constant{Name: name, SchemaName: schemaName, Description: cleanText(s.Description)}
	seen := map[string]string{}
	for _, v := range s.EnumValues() {
		cname := name + camel(v)
		if prev, ok := seen[cname]; ok {
			im.warn(fmt.Sprintf("%s: values %q and %q are both %s; %q skipped", name, prev, v, cname, v))
			continue
		}
		seen[cname] = v
		c.Values = append(c.Values, definitions.ConstantValue{Name: cname, Value: v})
	}

	return c
}

// unionModel records a oneOf/anyOf schema: the variants share no
// discriminator to decode on, so the model is raw JSON and names them.
func unionModel(name, schemaName string, s *openapi.Schema) *definitions.Model {
	m := &definitions.Model{Name: name, SchemaName: schemaName, Description: cleanText(s.Description)}
	for _, v := range append(append([]*openapi.Schema{}, s.OneOf...), s.AnyOf...) {
		if ref := v.RefName(); ref != "" {
			m.Union = append(m.Union, typeName(ref))
		} else {
			m.Union = append(m.Union, "<inline "+strings.TrimSpace(v.Type+" "+v.Format)+">")
		}
	}

	return m
}
