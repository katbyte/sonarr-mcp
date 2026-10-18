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
// zero or false, an event stream with at least one event), because an empty
// list decodes into any model and so proves nothing about its shape; and no
// object in it is lost whole, which is how a model whose fields do not match
// the server shows up (the JSON decoder drops a key it has no field for
// without a word). It does not check every field unless Strict asks: a server
// sends many its document does not declare, and a model that holds one of an
// object's keys holds that object.
//
// An event stream (text/event-stream) never ends, so the sweep does not read
// one whole: it reads as far as the first event and hangs up. One that sends
// no event in the time the sweep waits (EventWait) is an answer with nothing
// in it, like an empty list.
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
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/pandorest/definitions"
)

const (
	// DefaultTimeout is how long one operation gets when the sweep sets none.
	DefaultTimeout = 60 * time.Second
	// DefaultEventWait is how long an event stream gets to send its first
	// event when the sweep sets none.
	DefaultEventWait = 10 * time.Second

	// a JSON answer is buffered by the base client, so this only bounds a
	// read of what is already in memory; a file is streamed, and only its
	// start is read
	maxJSON = 64 << 20
	maxFile = 1 << 20
	// how many places a model dropped are named before the rest are counted
	maxDropped = 5

	eventStream = "text/event-stream"
)

// Case is how the sweep treats one operation.
type Case struct {
	// Skip leaves the operation uncalled, for the reason given.
	Skip string
	// Status is the error status the server answers with, and Decode is set
	// when it answers a body the documented model cannot decode; Why says
	// why that is the server's behaviour rather than a bug to fix. Refused
	// is the same for a call: the error name the server refuses it with
	// (EACCES for a role it does not have, ENOENT for a record it has none of).
	Status  int
	Refused string
	Decode  bool
	Why     string
	// Sometimes accepts an answer as well as the Status or the Decode, for a
	// call that depends on something outside the server (a catalogue it
	// fetches from the internet).
	Sometimes bool
	// Empty says why the operation answers with nothing on the fixtures (an
	// empty list, an object of zero values, a file of no bytes, an event
	// stream that sends no event while the sweep waits), which the sweep
	// otherwise fails.
	Empty string
	// MayBeEmpty says why the answer has something on one server image and
	// nothing on the next, so that neither is a failure.
	MayBeEmpty string
	// Path and Options supply arguments by parameter name and options field,
	// beyond what the fixtures resolve; Arguments does the same for a call,
	// by the argument's name as the server takes it, with a value of the
	// argument's own type or a string to read one from.
	Path      map[string]string
	Options   map[string]any
	Arguments map[string]any
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
	// method is called by name: the one Client of the flat layout, or the
	// client of the grouped one, whose field for the operation's tag has
	// the method.
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
	// MissingIDs are, per kind of id the service declares, a value of the right shape that nothing on the server has, and what
	// a missing one should get: the sweep calls a GET whose path takes the kind with it, fails when the answer is not the one
	// wanted, and notes what it was, so the service's own file says what a missing id gets rather than each tool learning it.
	MissingIDs map[string]Missing
	// Timeout is how long one operation gets; zero is DefaultTimeout.
	Timeout time.Duration
	// EventWait is how long an event stream gets, once it has answered, to
	// send its first event before the sweep hangs up and counts it as an
	// answer with nothing in it; zero is DefaultEventWait.
	EventWait time.Duration
}

// Missing is a value of one kind of id that nothing on the server has, and what a missing one should get.
type Missing struct {
	// ID is a value of the right shape that nothing has.
	ID string
	// Operation names the GET to probe with, by its generated name, so the probe goes through the route a tool relies on;
	// empty is the first GET whose path takes the kind.
	Operation string
	// Want is what the server should answer a missing id: a status such as "404", or "nothing" for a success with nothing
	// in it. Any other answer fails the run. Empty only notes what came back.
	Want string
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
	// empty is an answer with nothing in it that its case allows, and refused a status or an undecodable body its case expects
	empty, refused bool
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

	if s.Definitions == nil || s.Client == nil || (s.StatusCode == nil && !s.calls()) {
		t.Fatal("a sweep needs the service's definitions, its client and the base client's StatusCode")
	}

	report := Report{word: s.word()}
	cov := newCoverage(s)
	for _, op := range s.Definitions.Operations() {
		if !isRead(op) {
			continue
		}

		t.Run(op.Name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), cmp.Or(s.Timeout, DefaultTimeout))
			defer cancel()

			out := s.runOperation(ctx, op, cov)
			report.add(out)
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
		t.Errorf("the case for %s names no %s", name, strings.TrimSuffix(s.word(), "s"))
	}

	// what a missing id of each kind gets, failing where it is not what the service's file says, then what the run came to, and what
	// its answers left unproven
	for _, kind := range slices.Sorted(maps.Keys(s.MissingIDs)) {
		ctx, cancel := context.WithTimeout(t.Context(), cmp.Or(s.Timeout, DefaultTimeout))
		note, failure := s.probeMissing(ctx, kind, s.MissingIDs[kind])
		cancel()
		if failure != "" {
			t.Error(s.Definitions.Name + ": " + failure)
		}
		if note != "" {
			report.Notes = append(report.Notes, note)
		}
	}
	report.Notes = append(report.Notes, cov.notes()...)
	t.Log(report.Summary(s.Definitions.Name))
	for _, note := range report.Notes {
		t.Log(s.Definitions.Name + ": " + note)
	}
}

// probeMissing calls a GET whose path takes the kind with an id nothing has, the one named or else the first, and says what the
// server answered: a note of it, and a failure when it is not what is wanted or the probe could not be made.
func (s Sweep) probeMissing(ctx context.Context, kind string, m Missing) (note, failure string) {
	for _, op := range s.Definitions.Operations() {
		if op.Method != http.MethodGet || (m.Operation != "" && op.Name != m.Operation) {
			continue
		}
		for _, p := range op.PathParameters {
			if p.Type.Type != definitions.ID || p.Type.ReferenceName != kind {
				continue
			}
			c := s.Cases[op.Name]
			c.Path = maps.Clone(c.Path)
			if c.Path == nil {
				c.Path = map[string]string{}
			}
			c.Path[p.Name] = m.ID
			args, err := s.arguments(ctx, op, c)
			if err != nil {
				return "", fmt.Sprintf("a missing %s could not be probed through %s: %v", kind, op.Key(), err)
			}
			called, hangUp := context.WithCancel(ctx)
			a, err := s.call(called, hangUp, op, args, nil)
			hangUp()

			// what came back, in the words Want is written in
			got := "something"
			switch {
			case err != nil && a.status != 0:
				got = strconv.Itoa(a.status)
			case err != nil:
				return "", fmt.Sprintf("a missing %s fails %s: %v", kind, op.Key(), err)
			case a.empty:
				got = "nothing"
			}

			return missingVerdict(kind, op.Key(), got, m)
		}
	}
	if m.Operation != "" {
		return "", fmt.Sprintf("a missing %s was not probed: %s is no GET taking a %s in its path", kind, m.Operation, kind)
	}

	return fmt.Sprintf("no GET takes a %s in its path, so a missing one was not probed", kind), ""
}

// missingVerdict is the note of what a missing id of kind got through the operation key, and the failure when that is not
// what its Missing wants.
func missingVerdict(kind, key, got string, m Missing) (note, failure string) {
	note = fmt.Sprintf("a missing %s answers %s (%s)", kind, describeMissing(got), key)
	if m.Want != "" && got != m.Want {
		return note, fmt.Sprintf("%s, want %s", note, describeMissing(m.Want))
	}

	return note, ""
}

// describeMissing is a Want, or what a probe got, as words: "HTTP 404", "nothing" or "something".
func describeMissing(answer string) string {
	if _, err := strconv.Atoi(answer); err == nil {
		return "HTTP " + answer
	}

	return answer
}

// StaleCases names the cases that are for no GET operation of the service,
// which is what a case becomes when its operation is renamed or removed.
func (s Sweep) StaleCases() []string {
	gets := map[string]bool{}
	for _, op := range s.Definitions.Operations() {
		gets[op.Name] = isRead(op)
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
	return s.runOperation(ctx, op, nil)
}

// runOperation is Operation noting what the answer carried in cov, when there is one.
func (s Sweep) runOperation(ctx context.Context, op *definitions.Operation, cov *coverage) Outcome {
	var out Outcome
	c := s.Cases[op.Name]
	if c.Skip != "" {
		out.Skipped = c.Skip

		return out
	}

	// an event stream never ends, so the sweep ends it: cancelling the call's context is how it hangs up
	ctx, hangUp := context.WithCancel(ctx)
	defer hangUp()

	args, err := s.arguments(ctx, op, c)
	if err != nil {
		out.failf("%s: %v; resolve it in the fixtures or give it a case", op.Key(), err)

		return out
	}

	a, err := s.call(ctx, hangUp, op, args, cov)
	expectsFailure := c.Status != 0 || c.Refused != "" || c.Decode
	switch {
	case err == nil && expectsFailure && !c.Sometimes:
		out.failf("%s now answers; drop its case (%s)", op.Key(), c.Why)
	case err == nil:
		s.check(&out, op, c, a)
	case c.Status != 0 && a.status == c.Status:
		out.refused = true
		out.notef("%s: HTTP %d, as expected: %s", op.Key(), a.status, c.Why)
	case c.Refused != "" && a.refusal == c.Refused:
		out.refused = true
		out.notef("%s: refused with %s, as expected: %s", op.Key(), a.refusal, c.Why)
	case c.Decode && a.undecoded:
		out.refused = true
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
	method := s.method(op)
	if !method.IsValid() {
		return nil, errors.New("the client has no such method")
	}
	mt := method.Type()
	args := []reflect.Value{reflect.ValueOf(ctx)}

	if op.Call != nil {
		// a call's arguments in order: the case's, the fixtures' by name, and otherwise the zero value, which one the server
		// does not insist on may be left as
		if mt.NumIn() != len(op.Arguments)+1 {
			return nil, fmt.Errorf("the method takes %d arguments for a call of %d", mt.NumIn()-1, len(op.Arguments))
		}
		for i, a := range op.Arguments {
			v := reflect.New(mt.In(i + 1)).Elem()
			value, ok := c.Arguments[a.Name]
			if !ok {
				value, ok = lookupFold(s.Fixtures.Always, a.Name)
			}
			if !ok && a.Required {
				value, ok = lookupFold(s.Fixtures.Options, a.Name)
			}
			if !ok && a.Required {
				return nil, fmt.Errorf("no value for the argument %s", a.Name)
			}
			if ok {
				if err := setValue(v, value); err != nil {
					return nil, fmt.Errorf("argument %s: %w", a.Name, err)
				}
			}
			args = append(args, v)
		}

		return args, nil
	}

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
		optionsType := mt.In(len(args))
		if mt.IsVariadic() {
			// options that may all be left out are taken variadically; Call fills the slice from the one value
			optionsType = optionsType.Elem()
		}
		if optionsType.Kind() != reflect.Struct {
			// the one option is an argument (InlineSingleOptions), given when the case or the fixtures have a value for it
			o := &op.Options[0]
			value, ok, err := s.optionValue(o, c)
			if err != nil {
				return nil, err
			}
			switch {
			case ok && mt.IsVariadic() && o.Type.Type == definitions.List:
				// a list goes as its elements
				list := reflect.New(mt.In(len(args))).Elem()
				if err := setValue(list, value); err != nil {
					return nil, fmt.Errorf("option %s: %w", o.Name, err)
				}
				for i := range list.Len() {
					args = append(args, list.Index(i))
				}
			case ok:
				v := reflect.New(optionsType).Elem()
				if err := setValue(v, value); err != nil {
					return nil, fmt.Errorf("option %s: %w", o.Name, err)
				}
				args = append(args, v)
			}
			if len(args) != mt.NumIn() && !mt.IsVariadic() {
				return nil, fmt.Errorf("built %d arguments for a method that takes %d", len(args), mt.NumIn())
			}

			return args, nil
		}
		options := reflect.New(optionsType).Elem()
		for i := range op.Options {
			o := &op.Options[i]
			value, ok, err := s.optionValue(o, c)
			if err != nil {
				return nil, err
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

// optionValue is an option's value for a case: the case's own, else the
// fixtures' Always, else, for a required option, the fixtures' Options,
// without which a required option is an error.
func (s Sweep) optionValue(o *definitions.Option, c Case) (value any, ok bool, err error) {
	value, ok = c.Options[o.Field]
	if !ok {
		var always string
		always, ok = lookupFold(s.Fixtures.Always, o.Name)
		value = always
	}
	if !ok && o.Required {
		var required string
		required, ok = lookupFold(s.Fixtures.Options, o.Name)
		if !ok {
			return nil, false, fmt.Errorf("no value for required option %s", o.Name)
		}
		value = required
	}

	return value, ok, nil
}

// method finds the operation's method: on the client itself, or on the
// client's field named for the operation's group, where a call's method
// drops the group's name (c.Pool.Query).
func (s Sweep) method(op *definitions.Operation) reflect.Value {
	recv, name := s.receiver(op)
	if !recv.IsValid() {
		return reflect.Value{}
	}

	return recv.MethodByName(name)
}

// receiver is the value the operation's method is called on and the method's name: the client itself, or its field for the
// operation's group.
func (s Sweep) receiver(op *definitions.Operation) (recv reflect.Value, name string) {
	v := reflect.ValueOf(s.Client)
	if m := v.MethodByName(op.Name); m.IsValid() {
		return v, op.Name
	}
	for gi := range s.Definitions.Groups {
		grp := &s.Definitions.Groups[gi]
		if !slices.ContainsFunc(grp.Operations, func(o definitions.Operation) bool { return o.Name == op.Name }) {
			continue
		}
		f := reflect.Indirect(v).FieldByName(grp.Name)
		if !f.IsValid() {
			continue
		}
		if short := strings.TrimPrefix(op.Name, grp.Name); op.Call != nil && short != op.Name && f.MethodByName(short).IsValid() {
			return f, short
		}

		return f, op.Name
	}

	return reflect.Value{}, ""
}

// callOperation calls a read call through a capture of its connection and judges the answer as it came against what the
// method decoded: a refusal is the error name the server gave, and a decode failure an answer the model cannot read.
func (s Sweep) callOperation(op *definitions.Operation, args []reflect.Value, cov *coverage) (answer, error) {
	var a answer
	c := &capture{}
	method, err := s.callMethod(op, c)
	if err != nil {
		return a, err
	}
	results := method.Call(args)
	if len(results) != 2 || results[0].Kind() != reflect.Struct {
		return a, errors.New("the method does not return a result and an error, as a generated one does")
	}
	err, _ = reflect.TypeAssert[error](results[1])
	if err != nil {
		if refused := client.CallErrorOf(err); refused != nil {
			a.refusal = refused.ErrName
		}
		a.undecoded = a.refusal == "" && c.raw != nil

		return a, err
	}
	if op.Response == nil || c.raw == nil {
		return a, nil
	}

	return judgeJSON(c.raw, results[0].FieldByName("Answer"), cov), nil
}

// callMethod is a call's method on a copy of its client whose connection is wrapped in the capture, so the sweep reads the
// answer as it came as well as the method decoding it.
func (s Sweep) callMethod(op *definitions.Operation, c *capture) (reflect.Value, error) {
	recv, name := s.receiver(op)
	if !recv.IsValid() {
		return reflect.Value{}, errors.New("the client has no such method")
	}
	recv = reflect.Indirect(recv)
	held := recv.FieldByName("Caller")
	if !held.IsValid() || held.Kind() != reflect.Interface {
		return reflect.Value{}, errors.New("the client has no Caller, as a generated one over a connection does")
	}
	inner, ok := reflect.TypeAssert[client.Caller](held)
	if !ok || inner == nil {
		return reflect.Value{}, errors.New("the client's Caller is nil")
	}
	c.inner = inner
	wrapped := reflect.New(recv.Type()).Elem()
	wrapped.Set(recv)
	wrapped.FieldByName("Caller").Set(reflect.ValueOf(c))

	return wrapped.MethodByName(name), nil
}

// capture is a connection over the client's own that keeps each answer as it came, before the method decodes it.
type capture struct {
	inner client.Caller
	raw   json.RawMessage
}

func (c *capture) Call(ctx context.Context, method string, params []any, into any) error {
	c.raw = nil
	var raw json.RawMessage
	if err := c.inner.Call(ctx, method, params, &raw); err != nil {
		return err
	}
	c.raw = raw
	if into == nil {
		return nil
	}

	return json.Unmarshal(raw, into)
}

// isRead reports whether the sweep calls an operation: a GET, or a call that changes nothing.
func isRead(op *definitions.Operation) bool {
	return op.Method == http.MethodGet || (op.Call != nil && op.Call.Read)
}

// calls reports whether the service is one of calls over a connection, which has no HTTP status to go by.
func (s Sweep) calls() bool {
	for _, op := range s.Definitions.Operations() {
		if op.Call != nil {
			return true
		}
	}

	return false
}

// word is what the sweep calls what it sweeps, for the report.
func (s Sweep) word() string {
	if s.calls() {
		return "read calls"
	}

	return "GET operations"
}

// setValue sets a path argument or options field from a fixture: a string, a
// bool, a number, or a list.
func setValue(v reflect.Value, value any) error {
	if !v.IsValid() {
		return errors.New("no such field")
	}
	if given := reflect.ValueOf(value); given.IsValid() && given.Type().AssignableTo(v.Type()) {
		// a value of the argument's own type, as a case gives a call's filters or options
		v.Set(given)

		return nil
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
	case reflect.Struct:
		// a date (the package's Time or Day): decoded as the JSON string it is written as
		if err := json.Unmarshal([]byte(strconv.Quote(s)), v.Addr().Interface()); err != nil {
			return fmt.Errorf("cannot set %s from %q: %w", v.Type(), s, err)
		}
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
	// empty is an answer with nothing in it: no bytes, JSON holding no value
	// but empty ones, zeros and false, or an event stream that sent no event;
	// stream is set for an event stream, read only as far as its first event
	empty  bool
	stream bool
	// refusal is the error name a call was refused with, "" for one that was not
	refusal string
	// dropped are the places in the answer whose content the model lost
	// whole, and undeclared the keys it has no field for, as Model.key
	dropped    []string
	undeclared []string
	// unread are the dates no layout read, which the model kept as text,
	// and unknown the enum values the document does not list
	unread  []string
	unknown []string
}

// known is what a generated enum answers whether its value is listed by.
type known interface{ Known() bool }

// unknownValues walks a decoded model for enum values the document does not
// list, naming each by its place in the model and by its path in the answer
// as a workaround's In takes it: the JSON names, lists adding nothing.
func unknownValues(v reflect.Value, at, path string, found []string) []string {
	if !v.IsValid() {
		return found
	}
	if v.CanInterface() {
		if k, ok := reflect.TypeAssert[known](v); ok {
			if !k.Known() {
				found = append(found, fmt.Sprintf("%s = %q (%s, at %s)", at, v.String(), v.Type().Name(), inForm(path)))
			}

			return found
		}
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			found = unknownValues(v.Elem(), at, path, found)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if f.IsExported() {
				found = unknownValues(v.Field(i), at+"."+f.Name, pathJoin(path, jsonName(f)), found)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			found = unknownValues(v.Index(i), fmt.Sprintf("%s[%d]", at, i), path, found)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			found = unknownValues(v.MapIndex(k), fmt.Sprintf("%s[%v]", at, k), path, found)
		}
	default:
	}

	return found
}

// inForm is a path as a workaround's In takes it: a top-level name alone
// carries a leading dot, so that what is copied types that place and not
// every place of the name.
func inForm(path string) string {
	if !strings.Contains(path, ".") {
		return "." + path
	}

	return path
}

// jsonName is the name a field has on the wire.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "" {
		return f.Name
	}

	return name
}

// pathJoin is path with name under it, the name alone at the top.
func pathJoin(path, name string) string {
	if path == "" {
		return name
	}

	return path + "." + name
}

// unreadDate is what a generated Time or Day set to keep an unread date
// answers it by.
type unreadDate interface{ UnreadDate() string }

// unreadDates walks a decoded model for the dates no layout read.
func unreadDates(v reflect.Value, at string, found []string) []string {
	if !v.IsValid() {
		return found
	}
	if v.CanInterface() {
		if u, ok := reflect.TypeAssert[unreadDate](v); ok {
			if text := u.UnreadDate(); text != "" {
				found = append(found, fmt.Sprintf("%s = %q", at, text))
			}

			return found
		}
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			found = unreadDates(v.Elem(), at, found)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				found = unreadDates(v.Field(i), at+"."+v.Type().Field(i).Name, found)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			found = unreadDates(v.Index(i), fmt.Sprintf("%s[%d]", at, i), found)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			found = unreadDates(v.MapIndex(k), fmt.Sprintf("%s[%v]", at, k), found)
		}
	default:
	}

	return found
}

// call calls the operation, reads what it answered, and classifies a
// failure. hangUp cancels ctx, the context the call was given.
func (s Sweep) call(ctx context.Context, hangUp context.CancelFunc, op *definitions.Operation, args []reflect.Value, cov *coverage) (answer, error) {
	var a answer
	if op.Call != nil {
		return s.callOperation(op, args, cov)
	}
	results := s.method(op).Call(args)
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
	file := op.Response.Type.Type == definitions.RawFile
	if file && (isEventStream(op.Response.ContentType) || isEventStream(resp.Header.Get("Content-Type"))) {
		return s.firstEvent(ctx, hangUp, resp)
	}

	limit := int64(maxJSON)
	if file {
		limit = maxFile
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return a, fmt.Errorf("reading the answer: %w", err)
	}
	if file {
		a.empty = len(body) == 0

		return a, nil
	}

	return judgeJSON(body, results[0].FieldByName("Answer"), cov), nil
}

func isEventStream(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)

	return err == nil && mediaType == eventStream
}

// firstEvent reads an event stream as far as its first event and hangs up:
// read whole, as a file is, it would only run out of time. One that sends no
// event while the sweep waits, or ends without one, answered with nothing.
func (s Sweep) firstEvent(ctx context.Context, hangUp context.CancelFunc, resp *http.Response) (answer, error) {
	a := answer{stream: true}
	if sent := resp.Header.Get("Content-Type"); sent != "" && !isEventStream(sent) {
		return a, fmt.Errorf("answers %s where its document says %s", sent, eventStream)
	}

	timer := time.AfterFunc(cmp.Or(s.EventWait, DefaultEventWait), hangUp)
	defer timer.Stop()

	err := readEvent(resp.Body)
	switch {
	case err == nil:
		return a, nil
	case ctx.Err() != nil || errors.Is(err, io.EOF):
		a.empty = true

		return a, nil
	default:
		return a, fmt.Errorf("reading the event stream: %w", err)
	}
}

// readEvent reads an event stream to the end of its first event: the blank
// line after a block of lines with data among them. A comment (a line that
// starts with a colon, which a server sends to keep the connection open) is
// no event, and nor is a block with no data, which a browser would not
// deliver either.
func readEvent(r io.Reader) error {
	lines := bufio.NewReader(io.LimitReader(r, maxFile))
	data := false
	for {
		line, err := lines.ReadString('\n')
		if err != nil {
			return err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" && data {
			return nil
		}
		data = data || line == "data" || strings.HasPrefix(line, "data:")
	}
}

// check holds a successful answer to what the sweep claims of it: that it
// carries something, unless its case says why not, and that the model kept
// what it carries.
func (s Sweep) check(out *Outcome, op *definitions.Operation, c Case, a answer) {
	switch {
	case c.MayBeEmpty != "":
		out.empty = a.empty
		out.notef("%s: may answer with nothing (it did: %t): %s", op.Key(), a.empty, c.MayBeEmpty)
	case a.empty && c.Empty == "" && a.stream:
		out.failf("%s is an event stream that sent no event in %s, which proves only its status: point it at a fixture that sends one, or give it an Empty case", op.Key(), cmp.Or(s.EventWait, DefaultEventWait))
	case a.empty && c.Empty == "":
		out.failf("%s answers with nothing, which decodes into any model and so proves only its status: point it at a fixture that has something, or give it an Empty case", op.Key())
	case !a.empty && c.Empty != "":
		out.failf("%s now answers with something; drop its Empty case (%s)", op.Key(), c.Empty)
	case a.empty:
		out.empty = true
		out.notef("%s: answers with nothing, as expected: %s", op.Key(), c.Empty)
	case a.stream:
		out.notef("%s: an event stream, read as far as its first event and no further", op.Key())
	}

	for _, d := range a.dropped {
		out.failf("%s: the model drops what the server sent at %s", op.Key(), d)
	}
	if s.Strict && len(a.undeclared) > 0 {
		out.failf("%s answers fields the document does not declare: %s", op.Key(), strings.Join(a.undeclared, ", "))
	}
	for _, u := range a.unread {
		out.failf("%s answers a date no layout reads: %s; add the layout to the service's Dates or Days", op.Key(), u)
	}
	for _, u := range a.unknown {
		out.failf("%s answers a value its enum does not list: %s; add it to the enum's values, or narrow the enum's In to leave the place out", op.Key(), u)
	}
}

// judgeJSON reads a JSON answer against the model it was decoded into.
func judgeJSON(body []byte, model reflect.Value, cov *coverage) answer {
	var sent any
	if err := json.Unmarshal(body, &sent); err != nil {
		// text the model holds whole, or nothing at all
		return answer{empty: len(bytes.TrimSpace(body)) == 0}
	}

	a := answer{empty: emptyJSON(sent)}
	if !model.IsValid() {
		return a
	}
	cov.observe(sent, model)
	a.dropped = dropped(sent, model, "the answer")
	a.unread = unreadDates(model, "Answer", nil)
	a.unknown = unknownValues(model, "Answer", "", nil)
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

	if heldWhole(model.Type()) {
		return nil
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

var (
	rawMessage  = reflect.TypeFor[json.RawMessage]()
	unmarshaler = reflect.TypeFor[json.Unmarshaler]()
)

// heldWhole reports whether a type reads its JSON itself (a union holding the record as it came, a secret behind a placeholder,
// a date), so what it holds is not the sweep's to judge field by field.
func heldWhole(t reflect.Type) bool {
	return t == rawMessage || t.Implements(unmarshaler) || reflect.PointerTo(t).Implements(unmarshaler)
}

// undeclared walks a decoded JSON value beside the Go type it decodes into
// and records each object key the type has no field for, as Type.key.
func undeclared(t reflect.Type, sent any, seen map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if heldWhole(t) || t.Kind() == reflect.Interface {
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
