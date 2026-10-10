// Package sweep is the read sweep a repository runs against a real server:
// every GET operation in a service's definitions, called on the generated
// SDK with arguments resolved from the suite's fixtures, and its answer
// decoded (or its file read). Bespoke tests prove the shapes a tool relies on
// field by field; the sweep proves the rest of the read surface answers in its
// documented status with something in it, that what the server sends lands in
// the model, and that it keeps doing so as the definitions change: a GET the
// importer adds is swept on the next run with nothing to write, and one that
// fails must be classified.
//
// What the sweep holds an answer to: it decodes; it carries something (a
// file of at least one byte, JSON with at least one value that is not empty,
// zero or false), because an empty list decodes into any model and so proves
// nothing about its shape; and no object in it is lost whole, which is how a
// model whose fields do not match the server shows up (the JSON decoder drops
// a key it has no field for without a word). It does not check every field
// unless Strict asks: a server sends many its document does not declare, and
// a model that holds one of an object's keys holds that object.
//
// Every operation either answers with something, or has a Case that says why
// not: the feature needs something the test server lacks, the endpoint is
// gone from the server, the server answers a shape its document does not
// describe, or what the operation reads is not there on a fresh server. A
// Status, Decode or Empty case whose operation starts answering fails the
// sweep, so a stale one is noticed and removed, the way a stale importer
// workaround is. A Skip case is never called, so it is only ever reviewed by
// hand. Nothing is allowed to answer one way on some runs and another on
// others, with two ways out for what no test can set up: MayBeEmpty, for an
// answer that follows the server image, and Sometimes, for a call that
// depends on something outside the server.
package sweep

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/pandorest/definitions"
)

const (
	// DefaultTimeout is how long one operation gets when the sweep sets none.
	DefaultTimeout = 60 * time.Second

	// a JSON answer is buffered by the base client, so this only bounds a
	// read of what is already in memory; a file is streamed, and only its
	// start is read
	maxJSON = 64 << 20
	maxFile = 1 << 20
	// how many places a model dropped are named before the rest are counted
	maxDropped = 5
)

// Case is how the sweep treats one operation.
type Case struct {
	// Skip leaves the operation uncalled, for the reason given.
	Skip string
	// Status is the error status the server answers with, and Decode is set
	// when it answers a body the documented model cannot decode; Why says
	// why that is the server's behaviour rather than a bug to fix.
	Status int
	Decode bool
	Why    string
	// Sometimes accepts an answer as well as the Status or the Decode, for a
	// call that depends on something outside the server (a catalogue it
	// fetches from the internet).
	Sometimes bool
	// Empty says why the operation answers with nothing on the fixtures (an
	// empty list, an object of zero values, a file of no bytes), which the
	// sweep otherwise fails.
	Empty string
	// MayBeEmpty says why the answer has something on one server image and
	// nothing on the next, so that neither is a failure.
	MayBeEmpty string
	// Path and Options supply arguments by parameter name and options field,
	// beyond what the fixtures resolve.
	Path    map[string]string
	Options map[string]any
}

// Fixtures resolves arguments from the suite's fixtures. A path parameter is
// looked up in Path as "<segments>/<param>", the literal path segments before
// the placeholder and the placeholder's name (Items/Id, Users/Id), then as
// the name alone; all case-insensitively. The segments are as many as stand
// between the placeholder and the one before it, and the most of them wins:
// /api/v3/config/indexer/{id} is api/v3/config/indexer/id, then
// v3/config/indexer/id, config/indexer/id and indexer/id, so the settings
// under config can be given another id than the list of the same name. A
// required option is looked up by name in Options; Always holds options set
// whenever an operation has them (the user an API key has to name for
// anything user-scoped).
type Fixtures struct {
	Path    map[string]string
	Options map[string]string
	Always  map[string]string
}

func (f Fixtures) resolvePath(path, param string) (string, bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segments {
		if !strings.Contains(seg, "{"+param+"}") {
			continue
		}

		from := i
		for from > 0 && !strings.Contains(segments[from-1], "{") {
			from--
		}

		for ; from < i; from++ {
			if v, ok := lookupFold(f.Path, strings.Join(segments[from:i], "/")+"/"+param); ok {
				return v, true
			}
		}
	}

	return lookupFold(f.Path, param)
}

func lookupFold(m map[string]string, key string) (string, bool) {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}

	return "", false
}

// Sweep is one service's read sweep.
type Sweep struct {
	// Definitions are the service's, as definitions.Load reads them.
	Definitions *definitions.Service
	// Client is the generated package's client, on which each operation's
	// method is called by name.
	Client any
	// StatusCode is the base client's: the status of an error for a status
	// the operation does not document, and 0 for any other error.
	StatusCode func(error) int
	// Fixtures resolve the arguments, and Cases, by operation name, say how
	// each operation that cannot simply answer is treated.
	Fixtures Fixtures
	Cases    map[string]Case
	// Strict also fails an answer that carries a key its model has no field
	// for, by model and key. Without it only an object lost whole is a
	// failure, since servers send many fields their documents leave out.
	Strict bool
	// Timeout is how long one operation gets; zero is DefaultTimeout.
	Timeout time.Duration
}

// Outcome is what the sweep made of one operation.
type Outcome struct {
	// Skipped is why the operation was not called; empty when it was.
	Skipped string
	// Failures are what is wrong with the operation, its answer or its case;
	// none is a pass.
	Failures []string
	// Notes are what went as its case said it would.
	Notes []string
}

func (o *Outcome) failf(format string, args ...any) {
	o.Failures = append(o.Failures, fmt.Sprintf(format, args...))
}

func (o *Outcome) notef(format string, args ...any) {
	o.Notes = append(o.Notes, fmt.Sprintf(format, args...))
}

// Run sweeps every GET operation of the service, each as a subtest named for
// its method, and fails for a case that names no GET operation.
func (s Sweep) Run(t *testing.T) {
	t.Helper()

	if s.Definitions == nil || s.Client == nil || s.StatusCode == nil {
		t.Fatal("a sweep needs the service's definitions, its client and the base client's StatusCode")
	}

	for _, op := range s.Definitions.Operations() {
		if op.Method != http.MethodGet {
			continue
		}

		t.Run(op.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), cmp.Or(s.Timeout, DefaultTimeout))
			defer cancel()

			out := s.Operation(ctx, op)
			if out.Skipped != "" {
				t.Skip(out.Skipped)
			}
			for _, note := range out.Notes {
				t.Log(note)
			}
			for _, failure := range out.Failures {
				t.Error(failure)
			}
		})
	}

	for _, name := range s.StaleCases() {
		t.Errorf("the case for %s names no GET operation", name)
	}
}

// StaleCases names the cases that are for no GET operation of the service,
// which is what a case becomes when its operation is renamed or removed.
func (s Sweep) StaleCases() []string {
	gets := map[string]bool{}
	for _, op := range s.Definitions.Operations() {
		gets[op.Name] = op.Method == http.MethodGet
	}

	var stale []string
	for _, name := range slices.Sorted(maps.Keys(s.Cases)) {
		if !gets[name] {
			stale = append(stale, name)
		}
	}

	return stale
}

// Operation calls one operation and holds its answer to its case.
func (s Sweep) Operation(ctx context.Context, op *definitions.Operation) Outcome {
	var out Outcome
	c := s.Cases[op.Name]
	if c.Skip != "" {
		out.Skipped = c.Skip

		return out
	}

	args, err := s.arguments(ctx, op, c)
	if err != nil {
		out.failf("%s: %v; resolve it in the fixtures or give it a case", op.Key(), err)

		return out
	}

	a, err := s.call(op, args)
	expectsFailure := c.Status != 0 || c.Decode
	switch {
	case err == nil && expectsFailure && !c.Sometimes:
		out.failf("%s now answers; drop its case (%s)", op.Key(), c.Why)
	case err == nil:
		s.check(&out, op, c, a)
	case c.Status != 0 && a.status == c.Status:
		out.notef("%s: HTTP %d, as expected: %s", op.Key(), a.status, c.Why)
	case c.Decode && a.undecoded:
		out.notef("%s: does not decode, as expected: %s", op.Key(), c.Why)
	case a.undecoded:
		out.failf("%s answers what its model cannot decode: %v", op.Key(), err)
	default:
		out.failf("%s: %v", op.Key(), err)
	}

	return out
}

// arguments builds the call: the context, the path parameters, and an options
// struct with the required options and the case's options set.
func (s Sweep) arguments(ctx context.Context, op *definitions.Operation, c Case) ([]reflect.Value, error) {
	method := reflect.ValueOf(s.Client).MethodByName(op.Name)
	if !method.IsValid() {
		return nil, errors.New("the client has no such method")
	}
	mt := method.Type()
	args := []reflect.Value{reflect.ValueOf(ctx)}

	for _, p := range op.PathParameters {
		value, ok := c.Path[p.Name]
		if !ok {
			value, ok = s.Fixtures.resolvePath(op.Path, p.Name)
		}
		if !ok {
			return nil, fmt.Errorf("no value for path parameter {%s}", p.Name)
		}
		if len(args) >= mt.NumIn() {
			return nil, fmt.Errorf("the method takes no argument for path parameter {%s}", p.Name)
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
		if len(args) >= mt.NumIn() {
			return nil, errors.New("the method takes no options")
		}
		options := reflect.New(mt.In(len(args))).Elem()
		for _, o := range op.Options {
			value, ok := c.Options[o.Field]
			if !ok {
				var always string
				always, ok = lookupFold(s.Fixtures.Always, o.Name)
				value = always
			}
			if !ok && o.Required {
				var required string
				required, ok = lookupFold(s.Fixtures.Options, o.Name)
				if !ok {
					return nil, fmt.Errorf("no value for required option %s", o.Name)
				}
				value = required
			}
			if !ok {
				continue
			}
			if err := setValue(options.FieldByName(o.Field), value); err != nil {
				return nil, fmt.Errorf("option %s: %w", o.Name, err)
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
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		v.SetFloat(n)
	case reflect.Pointer:
		// a bool or a number: the options hold both as pointers, so 0 and
		// false can be asked for
		elem := reflect.New(v.Type().Elem())
		if err := setValue(elem.Elem(), value); err != nil {
			return fmt.Errorf("cannot set %s from %q: %w", v.Type(), s, err)
		}
		v.Set(elem)
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

// answer is what the sweep makes of a call.
type answer struct {
	// status is the status of an error for an undocumented one, and
	// undecoded is set for an error on an answer the server did give: it
	// answered in a documented status, but not the documented shape
	status    int
	undecoded bool
	// empty is an answer with nothing in it: no bytes, or JSON holding no
	// value but empty ones, zeros and false
	empty bool
	// dropped are the places in the answer whose content the model lost
	// whole, and undeclared the keys it has no field for, as Model.key
	dropped    []string
	undeclared []string
}

// call calls the operation, reads what it answered, and classifies a
// failure.
func (s Sweep) call(op *definitions.Operation, args []reflect.Value) (answer, error) {
	var a answer
	results := reflect.ValueOf(s.Client).MethodByName(op.Name).Call(args)
	if len(results) != 2 || results[0].Kind() != reflect.Struct {
		return a, errors.New("the method does not return a result and an error, as a generated one does")
	}
	held := results[0].FieldByName("HttpResponse")
	if !held.IsValid() {
		return a, errors.New("the method's result has no HttpResponse, as a generated one does")
	}
	resp, _ := reflect.TypeAssert[*http.Response](held)
	err, _ := reflect.TypeAssert[error](results[1])

	if err != nil {
		a.status = s.StatusCode(err)
		a.undecoded = a.status == 0 && resp != nil

		return a, err
	}
	if resp == nil || op.Response == nil {
		return a, nil
	}

	defer func() { _ = resp.Body.Close() }()
	limit := int64(maxJSON)
	if op.Response.Type.Type == definitions.RawFile {
		limit = maxFile
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return a, fmt.Errorf("reading the answer: %w", err)
	}
	if op.Response.Type.Type == definitions.RawFile {
		a.empty = len(body) == 0

		return a, nil
	}

	return judgeJSON(body, results[0].FieldByName("Model")), nil
}

// check holds a successful answer to what the sweep claims of it: that it
// carries something, unless its case says why not, and that the model kept
// what it carries.
func (s Sweep) check(out *Outcome, op *definitions.Operation, c Case, a answer) {
	switch {
	case c.MayBeEmpty != "":
		out.notef("%s: may answer with nothing (it did: %t): %s", op.Key(), a.empty, c.MayBeEmpty)
	case a.empty && c.Empty == "":
		out.failf("%s answers with nothing, which decodes into any model and so proves only its status: point it at a fixture that has something, or give it an Empty case", op.Key())
	case !a.empty && c.Empty != "":
		out.failf("%s now answers with something; drop its Empty case (%s)", op.Key(), c.Empty)
	case a.empty:
		out.notef("%s: answers with nothing, as expected: %s", op.Key(), c.Empty)
	}

	for _, d := range a.dropped {
		out.failf("%s: the model drops what the server sent at %s", op.Key(), d)
	}
	if s.Strict && len(a.undeclared) > 0 {
		out.failf("%s answers fields the document does not declare: %s", op.Key(), strings.Join(a.undeclared, ", "))
	}
}

// judgeJSON reads a JSON answer against the model it was decoded into.
func judgeJSON(body []byte, model reflect.Value) answer {
	var sent any
	if err := json.Unmarshal(body, &sent); err != nil {
		// text the model holds whole, or nothing at all
		return answer{empty: len(bytes.TrimSpace(body)) == 0}
	}

	a := answer{empty: emptyJSON(sent)}
	if !model.IsValid() {
		return a
	}
	a.dropped = dropped(sent, model, "the answer")
	if len(a.dropped) > maxDropped {
		a.dropped = append(a.dropped[:maxDropped], fmt.Sprintf("%d more places", len(a.dropped)-maxDropped))
	}
	seen := map[string]bool{}
	undeclared(model.Type(), sent, seen)
	a.undeclared = slices.Sorted(maps.Keys(seen))

	return a
}

// emptyJSON reports whether a decoded JSON value holds nothing but empty
// strings, lists and objects, zeros, false and null.
func emptyJSON(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case bool:
		return !v
	case float64:
		return v == 0
	case string:
		return v == ""
	case []any:
		return !slices.ContainsFunc(v, func(e any) bool { return !emptyJSON(e) })
	case map[string]any:
		for _, e := range v {
			if !emptyJSON(e) {
				return false
			}
		}

		return true
	}

	return false
}

// dropped walks a decoded JSON answer beside the model it was decoded into
// and names each place whose content the model lost whole: an object with
// something in it none of whose keys the model has a field for (the JSON
// decoder drops those without a word), or a list or map the model holds
// fewer entries of. A key the model lacks beside one it holds is not
// reported: servers send many fields their documents leave out.
func dropped(sent any, model reflect.Value, at string) []string {
	for model.Kind() == reflect.Pointer || model.Kind() == reflect.Interface {
		switch {
		case model.IsNil() && emptyJSON(sent):
			return nil
		case model.IsNil():
			return []string{at + " (the model holds nothing)"}
		case model.Kind() == reflect.Interface:
			return nil // a value of no declared type, held as it came
		}
		model = model.Elem()
	}

	var out []string
	switch a := sent.(type) {
	case map[string]any:
		keys := slices.Sorted(maps.Keys(a))
		switch model.Kind() {
		case reflect.Struct:
			fields := jsonFields(model.Type())
			var carried, kept []string
			for _, k := range keys {
				if emptyJSON(a[k]) {
					continue
				}
				carried = append(carried, k)
				i, ok := fields[strings.ToLower(k)]
				if !ok {
					continue
				}
				kept = append(kept, k)
				out = append(out, dropped(a[k], model.Field(i), at+"."+k)...)
			}
			if len(carried) > 0 && len(kept) == 0 {
				out = append(out, fmt.Sprintf("%s (the model has no field for any of %s)", at, strings.Join(carried, ", ")))
			}
		case reflect.Map:
			if model.Type().Key().Kind() != reflect.String {
				return nil
			}
			for _, k := range keys {
				v := model.MapIndex(reflect.ValueOf(k).Convert(model.Type().Key()))
				switch {
				case !v.IsValid() && !emptyJSON(a[k]):
					out = append(out, fmt.Sprintf("%s[%q] (missing from the model)", at, k))
				case v.IsValid():
					out = append(out, dropped(a[k], v, fmt.Sprintf("%s[%q]", at, k))...)
				}
			}
		default:
			// JSON held raw (json.RawMessage): nothing is dropped
		}
	case []any:
		// a byte slice is JSON held raw (json.RawMessage)
		if (model.Kind() != reflect.Slice && model.Kind() != reflect.Array) || model.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		if model.Len() < len(a) {
			return []string{fmt.Sprintf("%s (%d entries sent, %d held)", at, len(a), model.Len())}
		}
		for i, v := range a {
			out = append(out, dropped(v, model.Index(i), fmt.Sprintf("%s[%d]", at, i))...)
		}
	}

	return out
}

var rawMessage = reflect.TypeFor[json.RawMessage]()

// undeclared walks a decoded JSON value beside the Go type it decodes into
// and records each object key the type has no field for, as Type.key.
func undeclared(t reflect.Type, sent any, seen map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == rawMessage || t.Kind() == reflect.Interface {
		return
	}

	switch t.Kind() {
	case reflect.Struct:
		object, ok := sent.(map[string]any)
		if !ok {
			return
		}
		fields := jsonFields(t)
		for key, child := range object {
			i, ok := fields[strings.ToLower(key)]
			if !ok {
				seen[t.Name()+"."+key] = true
				continue
			}
			undeclared(t.Field(i).Type, child, seen)
		}
	case reflect.Slice, reflect.Array:
		list, ok := sent.([]any)
		if !ok {
			return
		}
		for _, e := range list {
			undeclared(t.Elem(), e, seen)
		}
	case reflect.Map:
		object, ok := sent.(map[string]any)
		if !ok {
			return
		}
		for _, e := range object {
			undeclared(t.Elem(), e, seen)
		}
	default:
	}
}

// jsonFields maps the lowercased JSON names of a struct's fields to their
// index, the way the JSON decoder matches keys (case-insensitively).
func jsonFields(t reflect.Type) map[string]int {
	fields := map[string]int{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		fields[strings.ToLower(name)] = i
	}

	return fields
}
