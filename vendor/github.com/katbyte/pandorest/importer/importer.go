// Package importer turns a vendored OpenAPI document into definitions, the
// way Pandora's importer-rest-api-specs does for Azure: load the document,
// apply the named workarounds for its known bugs, then normalise it into the
// definitions model (Go names, types, status codes, paging, and which tag
// owns each model).
//
// The importer is strict. Anything it cannot normalise faithfully (an
// undeclared path parameter, a GET that does not say what it answers, a
// duplicate operationId) fails the import with every problem listed, and the
// fix is a workaround that documents the bug (see the workarounds package).
package importer

import (
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/pandorest/config"
	"github.com/katbyte/pandorest/definitions"
	"github.com/katbyte/pandorest/importer/workarounds"
	"github.com/katbyte/pandorest/openapi"
)

// Import loads a service's document, applies the workarounds among all that
// are for it and returns the definitions. log receives one line per
// workaround applied and per warning.
func Import(cfg config.Service, all []workarounds.Workaround, log func(string)) (*definitions.Service, error) {
	spec, err := openapi.Load(cfg.Path(cfg.Spec))
	if err != nil {
		return nil, err
	}
	applied, err := workarounds.Apply(all, cfg.Name, spec, log)
	if err != nil {
		return nil, err
	}

	return FromSpec(cfg, spec, applied, log)
}

// FromSpec normalises an already patched document.
func FromSpec(cfg config.Service, spec *openapi.Spec, applied []string, log func(string)) (*definitions.Service, error) {
	if log == nil {
		log = func(string) {}
	}
	im := &importer{
		cfg:       cfg,
		spec:      spec,
		log:       log,
		models:    map[string]*definitions.Model{},
		constants: map[string]*definitions.Constant{},
		typeNames: map[string]string{},
	}

	im.checkWords()
	im.importSchemas()
	im.keepNull()
	groups := im.importOperations()
	for _, g := range groups {
		for i := range g.Operations {
			im.markPageable(&g.Operations[i])
		}
	}
	im.assignOwners(groups)

	if len(im.failures) > 0 {
		slices.Sort(im.failures)
		return nil, fmt.Errorf("%s: %s cannot be imported as it stands (fix each with a workaround):\n  %s",
			cfg.Name, cfg.Spec, strings.Join(im.failures, "\n  "))
	}

	svc := &definitions.Service{
		Name:        cfg.Name,
		Package:     cfg.Package,
		Title:       spec.Info.Title,
		APIVersion:  spec.Info.Version,
		Source:      cfg.Spec,
		Auth:        cfg.Auth,
		Workarounds: applied,
	}
	if svc.Workarounds == nil {
		svc.Workarounds = []string{}
	}
	for _, name := range openapi.SortedKeys(groups) {
		svc.Groups = append(svc.Groups, *groups[name])
	}
	if err := definitions.Validate(svc); err != nil {
		return nil, err
	}

	return svc, nil
}

// importer holds the state of one import.
type importer struct {
	cfg  config.Service
	spec *openapi.Spec
	log  func(string)

	models    map[string]*definitions.Model
	constants map[string]*definitions.Constant
	// typeNames maps every Go type name declared so far to where it came from
	typeNames map[string]string
	failures  []string
}

// checkWords holds the config's Words to spelling only: a key in lower case,
// and a value that is its key's letters, so a slip in one cannot give a
// method another name altogether.
func (im *importer) checkWords() {
	for seg, word := range im.cfg.Words {
		switch {
		case seg != strings.ToLower(seg):
			im.fail(fmt.Sprintf("the config's Words has the key %q, which is not lower case", seg))
		case !strings.EqualFold(word, seg):
			im.fail(fmt.Sprintf("the config's Words spells %q as %q, which is not the same letters", seg, word))
		}
	}
}

// keepNull marks the fields the service config names in KeepNull, which the
// generator holds as pointers. A field the document no longer has, or no
// longer declares a nullable number, fails the import: the config then names
// something the SDK cannot keep, and the entry must go or change.
func (im *importer) keepNull() {
	for _, name := range im.cfg.KeepNull {
		schema, prop, ok := strings.Cut(name, ".")
		var field *definitions.Field
		for _, m := range im.models {
			if ok && m.SchemaName == schema {
				field = fieldNamed(m.Fields, prop)
			}
		}
		switch {
		case field == nil:
			im.fail(fmt.Sprintf("the config's KeepNull names %s, a field the document does not have: change or remove the entry", name))
		case !field.Type.Numeric():
			im.fail(fmt.Sprintf("the config's KeepNull names %s, which is %s, not a number: change or remove the entry", name, field.Type))
		case !field.Nullable:
			im.fail(fmt.Sprintf("the config's KeepNull names %s, which the document no longer declares nullable: remove the entry", name))
		default:
			field.KeepsNull = true
		}
	}
}

// fieldNamed is the field of a model with a JSON name, or nil.
func fieldNamed(fields []definitions.Field, jsonName string) *definitions.Field {
	for i := range fields {
		if fields[i].JSONName == jsonName {
			return &fields[i]
		}
	}

	return nil
}

func (im *importer) warn(msg string) { im.log(im.cfg.Name + ": warning: " + msg) }

func (im *importer) fail(msg string) { im.failures = append(im.failures, msg) }

// assignOwners puts each model and constant in the group of the one tag whose
// operations use it, and those used by several tags (or none) in the common
// group. There is one package per server, so this decides file names only;
// BaseItemDto, used everywhere, is common.
func (im *importer) assignOwners(groups map[string]*definitions.Group) {
	users := map[string]map[string]bool{} // type name -> group names
	for name, g := range groups {
		seen := map[string]bool{}
		for i := range g.Operations {
			for _, t := range g.Operations[i].Types() {
				im.walk(t, seen)
			}
		}
		for typ := range seen {
			if users[typ] == nil {
				users[typ] = map[string]bool{}
			}
			users[typ][name] = true
		}
	}

	owner := func(typ string) *definitions.Group {
		if len(users[typ]) == 1 {
			for name := range users[typ] {
				return groups[name]
			}
		}
		if groups[definitions.CommonGroup] == nil {
			groups[definitions.CommonGroup] = &definitions.Group{Name: definitions.CommonGroup}
		}

		return groups[definitions.CommonGroup]
	}
	for _, name := range openapi.SortedKeys(im.models) {
		g := owner(name)
		g.Models = append(g.Models, *im.models[name])
	}
	for _, name := range openapi.SortedKeys(im.constants) {
		g := owner(name)
		g.Constants = append(g.Constants, *im.constants[name])
	}
}

// walk adds every model and constant a type reaches to seen.
func (im *importer) walk(t definitions.TypeRef, seen map[string]bool) {
	switch t.Type {
	case definitions.List, definitions.Dictionary:
		if t.NestedItem != nil {
			im.walk(*t.NestedItem, seen)
		}
	case definitions.Reference:
		if seen[t.ReferenceName] {
			return
		}
		seen[t.ReferenceName] = true
		if m := im.models[t.ReferenceName]; m != nil {
			for _, f := range m.Fields {
				im.walk(f.Type, seen)
			}
		}
	default:
	}
}
