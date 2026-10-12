package sweep

import (
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/pandorest/definitions"
)

// maxNamed is how many places or values a coverage note names before it says how many more there are.
const maxNamed = 20

// coverage is what a run's answers carried, for the notes at the end of a run: of the places an answer can hold an enum, a date or a
// boolean held by value, which ones a value came for, and of an enum's listed values, which ones no answer carried. A place is
// Model.Field, named as a model file declares it. What never came is not wrong, but it is unproven: a boolean held by value reads
// false whether the server left it out or sent false, and an enum value no answer carried may be one the server never sends.
type coverage struct {
	places map[string]placeKind
	// enums is the enum type of an enum place, and listed each enum type's values as the document lists them
	enums  map[string]string
	listed map[string][]string
	seen   map[string]bool
	// values are the values seen per enum type
	values map[string]map[string]bool
}

type placeKind int

const (
	enumPlace placeKind = iota + 1
	datePlace
	boolPlace
)

var (
	knownType = reflect.TypeFor[known]()
	timeType  = reflect.TypeFor[time.Time]()
)

// newCoverage finds every place an answer of a GET operation can hold an enum, a date or a boolean by value, through the answer types
// of the client's methods.
func newCoverage(s Sweep) *coverage {
	c := &coverage{places: map[string]placeKind{}, enums: map[string]string{}, listed: map[string][]string{}, seen: map[string]bool{}, values: map[string]map[string]bool{}}
	if s.Definitions != nil {
		for name, k := range s.Definitions.Constants() {
			for _, v := range k.Values {
				c.listed[name] = append(c.listed[name], v.Value)
			}
		}
	}
	visited := map[reflect.Type]bool{}
	for _, op := range s.Definitions.Operations() {
		if op.Method != http.MethodGet {
			continue
		}
		m := s.method(op)
		if !m.IsValid() || m.Type().NumOut() == 0 || m.Type().Out(0).Kind() != reflect.Struct {
			continue
		}
		if answer, ok := m.Type().Out(0).FieldByName("Answer"); ok {
			c.walkType(answer.Type, visited)
		}
	}

	return c
}

// walkType finds the places under a type once.
func (c *coverage) walkType(t reflect.Type, visited map[reflect.Type]bool) {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		c.walkType(t.Elem(), visited)
	case reflect.Struct:
		if visited[t] || isDate(t) {
			return
		}
		visited[t] = true
		for f := range t.Fields() {
			if !f.IsExported() || t.Name() == "" {
				continue
			}
			place := t.Name() + "." + f.Name
			switch kind, enum := classify(f.Type); kind {
			case enumPlace:
				c.places[place], c.enums[place] = kind, enum
			case datePlace, boolPlace:
				c.places[place] = kind
			default:
				c.walkType(f.Type, visited)
			}
		}
	default:
	}
}

// classify says what a field of the type is a place for, and the enum type's name for an enum: a boolean counts only by value, since a
// pointer one tells absent from false by itself.
func classify(t reflect.Type) (kind placeKind, enum string) {
	base := t
	if base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	switch {
	case base.Kind() == reflect.String && base.Implements(knownType):
		return enumPlace, base.Name()
	case isDate(base):
		return datePlace, ""
	case t.Kind() == reflect.Bool:
		return boolPlace, ""
	}

	return 0, ""
}

// isDate reports whether a type is a generated Time, Day, Unix or UnixMilli: a struct around time.Time.
func isDate(t reflect.Type) bool {
	if t.Kind() != reflect.Struct || t.NumField() == 0 {
		return false
	}
	f := t.Field(0)

	return f.Anonymous && f.Type == timeType
}

// observe walks a decoded answer beside the model it was decoded into and marks the places a value came for.
func (c *coverage) observe(sent any, v reflect.Value) {
	if c == nil || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			c.observe(sent, v.Elem())
		}
	case reflect.Struct:
		m, ok := sent.(map[string]any)
		if !ok || isDate(v.Type()) {
			return
		}
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			raw, present := m[jsonName(f)]
			if !present || raw == nil {
				continue
			}
			place := v.Type().Name() + "." + f.Name
			switch kind, enum := classify(f.Type); kind {
			case enumPlace:
				if text, ok := raw.(string); ok && text != "" {
					c.seen[place] = true
					if c.values[enum] == nil {
						c.values[enum] = map[string]bool{}
					}
					c.values[enum][text] = true
				}
			case datePlace:
				text, isText := raw.(string)
				if _, isNumber := raw.(float64); isNumber || (isText && text != "") {
					c.seen[place] = true
				}
			case boolPlace:
				c.seen[place] = true
			default:
				c.observe(raw, v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		list, ok := sent.([]any)
		if !ok {
			return
		}
		for i := range min(len(list), v.Len()) {
			c.observe(list[i], v.Index(i))
		}
	case reflect.Map:
		m, ok := sent.(map[string]any)
		if !ok || v.Type().Key().Kind() != reflect.String {
			return
		}
		for _, k := range v.MapKeys() {
			c.observe(m[k.String()], v.MapIndex(k))
		}
	default:
	}
}

// notes are what the run proved and what it left unproven: per enum type the places a value came for and the listed values none
// carried, then the same for dates and for booleans held by value. A kind with no place says nothing.
func (c *coverage) notes() []string {
	var out []string
	byEnum := map[string][]string{}
	for place, kind := range c.places {
		if kind == enumPlace {
			byEnum[c.enums[place]] = append(byEnum[c.enums[place]], place)
		}
	}
	for _, enum := range slices.Sorted(maps.Keys(byEnum)) {
		line := fmt.Sprintf("enum %s: %s", enum, c.reached(byEnum[enum], "a value seen at"))
		var never []string
		for _, v := range c.listed[enum] {
			if !c.values[enum][v] {
				never = append(never, v)
			}
		}
		if len(never) > 0 && len(c.values[enum]) > 0 {
			line += "; listed values never seen: " + named(never)
		}
		out = append(out, line)
	}
	if dates := c.of(datePlace); len(dates) > 0 {
		out = append(out, "dates: "+c.reached(dates, "a value seen at"))
	}
	if bools := c.of(boolPlace); len(bools) > 0 {
		out = append(out, "booleans held by value: "+c.reached(bools, "carried by an answer at"))
	}

	return out
}

// of lists the places of a kind.
func (c *coverage) of(kind placeKind) []string {
	var out []string
	for place, k := range c.places {
		if k == kind {
			out = append(out, place)
		}
	}

	return out
}

// reached words how many of the places a value came for, naming the ones none did.
func (c *coverage) reached(places []string, seen string) string {
	slices.Sort(places)
	var never []string
	for _, p := range places {
		if !c.seen[p] {
			never = append(never, p)
		}
	}
	// a place is a field of a record a GET answers, so a model no GET answers is not among them
	line := fmt.Sprintf("%s in what a GET answers, %s %d", placesWord(len(places)), seen, len(places)-len(never))
	if len(never) > 0 && len(never) < len(places) {
		line += " (never: " + named(never) + ")"
	}

	return line
}

// named is a list of names, cut after maxNamed with how many more there are.
func named(names []string) string {
	if len(names) <= maxNamed {
		return strings.Join(names, ", ")
	}

	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxNamed], ", "), len(names)-maxNamed)
}

func placesWord(n int) string {
	if n == 1 {
		return "1 place"
	}

	return fmt.Sprintf("%d places", n)
}

// Report is what a run of the sweep came to, operation by operation and over the answers as a whole.
type Report struct {
	// Answered is the operations that answered as their model says; Empty those that answered nothing, as their Empty case says;
	// Refused those that answered the status or the undecodable body their case says; Skipped and Failed the rest.
	Answered, Empty, Refused, Skipped, Failed int
	// Notes are what the run's answers carried of the places an answer can hold an enum, a date or a boolean held by value, and
	// which listed enum values none carried: unproven rather than wrong.
	Notes []string
}

// Summary is the counts in one line.
func (r *Report) Summary(service string) string {
	return fmt.Sprintf("%s: %d GET operations: %d answered, %d answered nothing by their Empty case, %d refused as their case says, %d skipped, %d failed", service, r.Answered+r.Empty+r.Refused+r.Skipped+r.Failed, r.Answered, r.Empty, r.Refused, r.Skipped, r.Failed)
}

// add counts one operation's outcome.
func (r *Report) add(out Outcome) {
	switch {
	case out.Skipped != "":
		r.Skipped++
	case len(out.Failures) > 0:
		r.Failed++
	case out.empty:
		r.Empty++
	case out.refused:
		r.Refused++
	default:
		r.Answered++
	}
}

// NeverCalled is what a live run left unreached: the operations none of the
// calls recorded (client.WithCalls) were for, each of which must be excused
// by name with a reason; an excused operation the run called after all, so
// the excuse is stale; and an excuse that names no operation. Every line is
// a problem for the run to fail on; none is a run that reached everything it
// did not excuse. A call a generated method sent names its operation; one
// written by hand is given to the single most literal operation its method
// and path fit, so a lookup under /series does not count for /series/{id}. A
// call's path may carry a base path before the template's segments.
func NeverCalled(defs *definitions.Service, calls []client.Call, excused map[string]string) []string {
	ops := defs.Operations()
	called := map[string]bool{}
	for _, c := range calls {
		if c.Operation != "" {
			called[c.Operation] = true

			continue
		}
		if op := mostLiteral(ops, c); op != nil {
			called[op.Name] = true
		}
	}

	var problems []string
	names := map[string]bool{}
	for _, op := range ops {
		names[op.Name] = true
		switch {
		case called[op.Name] && excused[op.Name] != "":
			problems = append(problems, fmt.Sprintf("%s (%s) is excused but was called, so drop its excuse (%s)", op.Name, op.Key(), excused[op.Name]))
		case !called[op.Name] && excused[op.Name] == "":
			problems = append(problems, fmt.Sprintf("%s (%s) was never called, and is not excused", op.Name, op.Key()))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(excused)) {
		if !names[name] {
			problems = append(problems, fmt.Sprintf("the excuse for %s names no operation", name))
		}
	}

	return problems
}

// mostLiteral is the operation a hand-written call was for: of those its method and path fit, the one with the fewest placeholders,
// or nil when none fits.
func mostLiteral(ops []*definitions.Operation, c client.Call) *definitions.Operation {
	var best *definitions.Operation
	for _, op := range ops {
		if op.Command != "" || c.Method != op.Method || !pathMatches(op.Path, c.Path) {
			continue
		}
		if best == nil || strings.Count(op.Path, "{") < strings.Count(best.Path, "{") {
			best = op
		}
	}

	return best
}

// pathMatches reports whether a path a request went to is the template's,
// its placeholders standing for one segment each, with any base path before.
func pathMatches(template, path string) bool {
	want := strings.Split(strings.Trim(template, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(got) < len(want) {
		return false
	}
	got = got[len(got)-len(want):]
	for i, w := range want {
		placeholder := strings.HasPrefix(w, "{") && strings.HasSuffix(w, "}")
		if !placeholder && w != got[i] {
			return false
		}
	}

	return true
}
