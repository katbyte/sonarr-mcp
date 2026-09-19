package definitions

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Types lists every type an operation mentions directly.
func (o *Operation) Types() []TypeRef {
	var out []TypeRef
	for _, p := range o.PathParameters {
		out = append(out, p.Type)
	}
	for _, opt := range o.Options {
		out = append(out, opt.Type)
	}
	if o.Request != nil {
		out = append(out, o.Request.Type)
	}
	if o.Response != nil {
		out = append(out, o.Response.Type)
	}

	return out
}

// Validate checks a service's definitions are self-consistent: every
// reference resolves, and no two operations share a name or a method and
// path. The importer runs it on what it produces and the generator on what it
// reads, so hand-edited definitions fail before any code is written.
func Validate(svc *Service) error {
	var problems []string
	models, constants := svc.Models(), svc.Constants()
	var check func(where string, t TypeRef)
	check = func(where string, t TypeRef) {
		switch t.Type {
		case Reference:
			if models[t.ReferenceName] == nil && constants[t.ReferenceName] == nil {
				problems = append(problems, fmt.Sprintf("%s: reference to undefined type %q", where, t.ReferenceName))
			}
		case List, Dictionary:
			if t.NestedItem == nil {
				problems = append(problems, where+": "+string(t.Type)+" without a nested item")
				return
			}
			check(where, *t.NestedItem)
		case Boolean, Integer, Integer64, Float, Double, String, RawObject, Any, RawFile:
		default:
			problems = append(problems, fmt.Sprintf("%s: unknown type %q", where, t.Type))
		}
	}

	names, keys := map[string]bool{}, map[string]bool{}
	for _, o := range svc.Operations() {
		if names[o.Name] {
			problems = append(problems, "two operations are named "+o.Name)
		}
		if keys[o.Key()] {
			problems = append(problems, "two operations are "+o.Key())
		}
		names[o.Name], keys[o.Key()] = true, true
		for i, t := range o.Types() {
			check(fmt.Sprintf("%s type %d", o.Name, i), t)
		}
		if len(o.ExpectedStatusCodes) == 0 {
			problems = append(problems, o.Name+": no expected status codes")
		}
	}
	for name, m := range models {
		for _, f := range m.Fields {
			check(name+"."+f.Name, f.Type)
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return errors.New(svc.Name + ": invalid definitions:\n  " + strings.Join(problems, "\n  "))
	}

	return nil
}
