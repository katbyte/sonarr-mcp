// Package registry is how a katbyte MCP tool decides which of its tools a
// session gets, and what each tells a client about itself.
//
// Tools are named resource-first (library_*, item_*, audit_*) and each is
// added with a kind: a read tool never changes what the server holds, a
// write tool does, and a delete tool removes what cannot be put back. A
// read-only session gets the read tools alone; the delete tools are held
// back until the operator asks for them; toolsets pick the groups a session
// needs, so a client loads a working subset rather than every definition; an
// allow list adds tools by name to whatever the toolsets hold; and a deny
// list takes tools out of what is left.
//
// What is shared is the machinery. The tools, the toolsets they sit in and
// the hints each carries are the application's, given in a Config.
package registry

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/katbyte/go-kt/chttp"
	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/outside"
)

// Kind is what a tool does to the server it works on.
type Kind int

const (
	// Read never changes what the server holds.
	Read Kind = iota
	// Write changes it, and is left out of a read-only session.
	Write
	// Delete removes what cannot be put back, and is registered only when
	// the operator asks for the delete tools.
	Delete
)

func (k Kind) String() string {
	return [...]string{"read", "write", "delete"}[k]
}

// Core is the toolset every other assumes - enough to find things and open
// them - and is added to whatever sets a session asks for, when the
// application has one of that name.
const Core = "core"

// All is the toolset name that asks for every tool.
const All = "all"

// Essential is the name an allow list asks for the application's preset by.
const Essential = "essential"

// Hints are what a tool tells a client about itself beyond its kind, in the
// terms of the MCP annotations. The zero value is the answer that warns: a
// write tool with no hints of its own is marked destructive.
type Hints struct {
	// Additive is for a write tool that only ever adds - a new library, a
	// new playlist - and never changes or takes away what was there: a
	// value written over, a file replaced, a history cleared. MCP reads
	// "not destructive" as exactly that.
	Additive bool
	// Idempotent is for a write tool that changes nothing more when called
	// again with the same arguments.
	Idempotent bool
	// SendsOut is for a tool that itself sends something to a person or a
	// service beyond the server it works on: an email, a notification. A
	// tool that makes the server fetch from its own providers, or hand
	// work to its own download client, does not. MCP's name for the hint
	// is "open world".
	SendsOut bool
	// Installs is for a tool that has the server bring something in from
	// outside and run it: an app, a plug-in. What it brings in is someone
	// else's to change, and MCP has the one hint, "open world", for that
	// and for SendsOut: a tool that sets either carries it.
	Installs bool
	// WritesHere is for a read tool that writes a file on the machine it
	// runs on. The server is only read, so it stays a read tool and a
	// read-only session keeps it, but it does not claim to change nothing.
	WritesHere bool
}

// Config is the application's side of a registry.
type Config struct {
	// Toolsets are the curated groups by name, each tool in one of them.
	// The one named Core is added to whatever else is asked for.
	Toolsets map[string][]string
	// Essential is the preset an allow list names as "essential".
	Essential []string
	// Hints are the tools' hints by name.
	Hints map[string]Hints
	// LogError is where a handler's panic and its stack are written; nil is
	// clog.Log.Errorf, which writes to stderr - stdout carries the protocol
	// itself when serving stdio.
	LogError func(format string, args ...any)
	// LogWrite is where one line is written for every call of a write or a
	// delete tool: the tool, what the call sent with its credentials
	// blanked, and what became of it. nil is clog.Log.Infof.
	LogWrite func(format string, args ...any)
}

// Selection is what one session asks for.
type Selection struct {
	// ReadOnly keeps the read tools alone.
	ReadOnly bool
	// EnableDelete lets the delete tools in.
	EnableDelete bool
	// Toolsets are the sets asked for, each entry a name or several joined
	// by commas: a curated set, All, or a resource family (every tool whose
	// name begins with it, "library" for library_*). None is every tool.
	Toolsets []string
	// Allow asks for tools by name: exact names, a pattern with one "*" at
	// either end (library_*, *_delete), or Essential. Beside Toolsets it
	// adds to them, "these sets and these tools as well"; on its own it is
	// only the tools it names, with no Core added. It lets nothing past
	// ReadOnly or EnableDelete.
	Allow []string
	// Deny takes out the tools it names, the same way, from whatever the
	// toolsets and the allow list asked for: it is how a toolset is
	// narrowed.
	Deny []string
}

// Info describes a tool a selection would register.
type Info struct {
	Name string
	// Kind is read, write or delete
	Kind string
	// Toolset is the curated set the tool belongs to
	Toolset     string
	Description string
}

type pending struct {
	name        string
	kind        Kind
	description string
	// arguments are the names of the arguments the tool takes, in the
	// order its input declares them
	arguments []string
	register  func(*mcp.Server)
}

// Registry collects an application's tools before any is registered, so a
// selection can be checked against all of them: a pattern that names no
// tool is refused rather than quietly hiding one.
type Registry struct {
	cfg     Config
	pending []pending
}

// New makes an empty registry.
func New(cfg Config) *Registry { return &Registry{cfg: cfg} }

// Add queues a typed tool. It sets the tool's MCP annotations from its kind
// and its hints, so a client can tell a read from a destructive write
// without parsing descriptions; turns a panic in the handler into an
// ordinary tool error; and sends every empty collection in an answer as []
// rather than null, which a client cannot tell from "not fetched".
func Add[In, Out any](r *Registry, kind Kind, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	hints := r.cfg.Hints[t.Name]
	openWorld := hints.SendsOut || hints.Installs
	switch kind {
	case Read:
		t.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: !hints.WritesHere, DestructiveHint: new(false), OpenWorldHint: new(openWorld)}
	case Write:
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: new(!hints.Additive), IdempotentHint: hints.Idempotent, OpenWorldHint: new(openWorld)}
	case Delete:
		t.Annotations = &mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: new(openWorld)}
	}

	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		res, out, err := recovered(ctx, r, t.Name, h, req, in)
		if err == nil {
			emptyNilSlices(reflect.ValueOf(&out).Elem())
		}

		return res, out, err
	}

	r.pending = append(r.pending, pending{
		name:        t.Name,
		kind:        kind,
		description: t.Description,
		arguments:   argumentNames(reflect.TypeFor[In]()),
		register:    func(server *mcp.Server) { mcp.AddTool(server, t, wrapped) },
	})
}

// argumentNames is the arguments a tool takes: the JSON names of its input's
// fields, in the order they are declared, those of an embedded struct among
// them. An input that is no struct names none.
func argumentNames(t reflect.Type) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}

	var names []string
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-", !f.IsExported() && !f.Anonymous:
		case f.Anonymous && name == "":
			names = append(names, argumentNames(f.Type)...)
		case name == "":
			names = append(names, f.Name)
		default:
			names = append(names, name)
		}
	}

	return names
}

// recovered calls a handler, turning a panic into an ordinary tool error.
// Nothing above the handler recovers one - not the MCP SDK, not a CLI - so
// one nil dereference in one tool would otherwise end the whole session. The
// caller is told which tool failed, and the stack goes to the log.
func recovered[In, Out any](ctx context.Context, r *Registry, name string, h mcp.ToolHandlerFor[In, Out], req *mcp.CallToolRequest, in In) (res *mcp.CallToolResult, out Out, err error) {
	defer func() {
		if p := recover(); p != nil {
			r.logError("internal error in %s: %v\n%s", name, p, debug.Stack())
			var zero Out
			res, out, err = nil, zero, fmt.Errorf("internal error in %s: %v", name, p)
		}
	}()

	return h(ctx, req, in)
}

func (r *Registry) logError(format string, args ...any) {
	if r.cfg.LogError != nil {
		r.cfg.LogError(format, args...)

		return
	}
	clog.Log.Errorf(format, args...)
}

// emptyNilSlices walks v (structs, pointers, slices) and replaces every
// settable nil slice with an empty one.
func emptyNilSlices(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			emptyNilSlices(v.Elem())
		}
	case reflect.Struct:
		for _, f := range v.Fields() {
			emptyNilSlices(f)
		}
	case reflect.Slice:
		if v.IsNil() {
			if v.CanSet() {
				v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			}

			return
		}
		for i := range v.Len() {
			emptyNilSlices(v.Index(i))
		}
	default:
	}
}

// Register adds every tool the selection permits to the server and answers
// with the names registered, sorted. It fails when the selection names a
// toolset or a pattern that reaches no tool, so a typo cannot hide one.
func (r *Registry) Register(server *mcp.Server, sel Selection) ([]string, error) {
	keep, err := r.selected(sel)
	if err != nil {
		return nil, err
	}

	var registered []string
	tools := map[string]pending{}
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		p.register(server)
		registered = append(registered, p.name)
		tools[p.name] = p
	}
	slices.Sort(registered)
	server.AddReceivingMiddleware(r.calls(tools))

	return registered, nil
}

// The ways a call of a write or a delete tool ends, as LogWrite names them.
// Whether a call that was answered changed anything or showed what it would
// is the tool's to say, with an argument of its own such as confirm or
// dry_run, which the line carries.
const (
	// outcomeRefused is a call whose arguments were turned away before the
	// tool ran.
	outcomeRefused = "refused"
	// outcomeFailed is a call the tool ran and answered with an error.
	outcomeFailed = "failed"
	// outcomeAnswered is a call the tool ran and answered.
	outcomeAnswered = "answered"
)

// The most of a call's arguments, and of an error, that a line of LogWrite
// carries.
const (
	loggedArguments = 2000
	loggedError     = 300
)

// calls stands between a client and every call of a tool, which is the one
// place that sees a call whose arguments never reached the tool. It tells a
// caller who sent an argument the tool does not take which ones it does,
// and it writes a line for every call of a write or a delete tool.
func (r *Registry) calls(tools map[string]pending) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || call.Params == nil {
				return next(ctx, method, req)
			}
			p, known := tools[call.Params.Name]
			if !known {
				return next(ctx, method, req)
			}

			result, err := next(ctx, method, req)
			outcome, failure := outcomeAnswered, err
			if err != nil {
				outcome = outcomeFailed
			}
			if res, answered := result.(*mcp.CallToolResult); answered && err == nil && res != nil && res.IsError {
				outcome, failure = outcomeFailed, res.GetError()
				if failure != nil && strings.HasPrefix(failure.Error(), `validating "arguments":`) {
					outcome = outcomeRefused
					nameArguments(res, p)
				}
			}
			if p.kind != Read {
				r.logWrite(p, call.Params.Arguments, outcome, failure)
			}

			return result, err
		}
	}
}

// nameArguments adds the arguments a tool takes to its refusal of one it
// does not: the SDK says which argument was not expected, and a caller that
// mistyped offset is not helped by that alone.
func nameArguments(res *mcp.CallToolResult, p pending) {
	refusal := res.GetError().Error()
	if !strings.Contains(refusal, "unexpected additional properties") {
		return
	}

	takes := "no arguments"
	switch n := len(p.arguments); {
	case n == 1:
		takes = p.arguments[0]
	case n > 1:
		takes = strings.Join(p.arguments[:n-1], ", ") + " and " + p.arguments[n-1]
	}
	res.Content = []mcp.Content{&mcp.TextContent{Text: refusal + ": " + p.name + " takes " + takes}}
}

// logWrite writes the line for one call of a write or a delete tool. What
// the call sent is written with its credentials blanked, by the rule a trace
// of a request hides them by, and with nothing in it that does not show, so
// a line is a line whatever was sent.
func (r *Registry) logWrite(p pending, arguments json.RawMessage, outcome string, failure error) {
	log := r.cfg.LogWrite
	if log == nil {
		log = clog.Log.Infof
	}

	sent := "{}"
	if len(arguments) > 0 {
		sent = outside.Text(chttp.RedactJSON(string(arguments)), loggedArguments)
	}
	if failure != nil {
		log("%s %s %s %s: %s", p.kind, p.name, outcome, sent, outside.Text(failure.Error(), loggedError))

		return
	}
	log("%s %s %s %s", p.kind, p.name, outcome, sent)
}

// Describe lists the tools a selection would register, by name, with
// nothing registered: the same choice Register makes, so what a tool says it
// would serve cannot drift from what it serves.
func (r *Registry) Describe(sel Selection) ([]Info, error) {
	keep, err := r.selected(sel)
	if err != nil {
		return nil, err
	}

	set := map[string]string{}
	for name, members := range r.cfg.Toolsets {
		for _, m := range members {
			// core wins: it is the set a tool is reached through most often
			if set[m] == "" || name == Core {
				set[m] = name
			}
		}
	}

	out := make([]Info, 0, len(r.pending))
	for _, p := range r.pending {
		if !keep[p.name] {
			continue
		}
		out = append(out, Info{Name: p.name, Kind: p.kind.String(), Toolset: set[p.name], Description: p.description})
	}
	slices.SortFunc(out, func(a, b Info) int { return cmp.Compare(a.Name, b.Name) })

	return out, nil
}

// Names is every tool added, whatever a selection would make of it, in the
// order added.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.pending))
	for _, p := range r.pending {
		out = append(out, p.name)
	}

	return out
}

// ToolsetNames lists the curated toolsets, sorted, for help output.
func (r *Registry) ToolsetNames() []string {
	out := make([]string, 0, len(r.cfg.Toolsets))
	for k := range r.cfg.Toolsets {
		out = append(out, k)
	}
	slices.Sort(out)

	return out
}

// FamilyNames lists the resource families a toolset entry may name - the
// part of each tool's name before its first underscore - sorted. They are
// read off the tools added, so they cannot go stale.
func (r *Registry) FamilyNames() []string { return families(r.Names()) }

// selected applies the kind gates, then what the toolsets and the allow list
// ask for between them, then the deny list. Toolsets and an allow list both
// ask for tools, and a session gets what either names; with neither it gets
// every tool.
func (r *Registry) selected(sel Selection) (map[string]bool, error) {
	names := r.Names()
	sets, err := r.compileToolsets(sel.Toolsets, names)
	if err != nil {
		return nil, err
	}
	allow, err := r.compilePatterns(sel.Allow, names, "allow")
	if err != nil {
		return nil, err
	}
	deny, err := r.compilePatterns(sel.Deny, names, "deny")
	if err != nil {
		return nil, err
	}
	// what was asked for is read off the lists as they were given: an allow
	// list of a preset the application left empty asks for nothing, which
	// is not the same as no allow list
	everything := len(entries(sel.Toolsets)) == 0 && len(entries(sel.Allow)) == 0

	keep := make(map[string]bool, len(r.pending))
	for _, p := range r.pending {
		switch {
		case p.kind == Delete && !sel.EnableDelete:
		case p.kind != Read && sel.ReadOnly:
		case !everything && !sets[p.name] && !matchesAny(allow, p.name):
		case matchesAny(deny, p.name):
		default:
			keep[p.name] = true
		}
	}

	return keep, nil
}

// entries splits a list whose entries may each hold several names joined
// by commas, as a flag given twice and an environment variable both arrive.
func entries(raw []string) []string {
	var out []string
	for _, entry := range raw {
		for name := range strings.SplitSeq(entry, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
	}

	return out
}

// compileToolsets turns the set names asked for into the tools they hold,
// always with the core set. An unknown name is an error naming the valid
// ones, the way a pattern that matches nothing is.
func (r *Registry) compileToolsets(raw, known []string) (map[string]bool, error) {
	asked := entries(raw)
	if len(asked) == 0 {
		return nil, nil
	}

	out := map[string]bool{}
	for _, name := range asked {
		if name == All {
			for _, t := range known {
				out[t] = true
			}

			continue
		}
		if tools, ok := r.cfg.Toolsets[name]; ok {
			for _, t := range tools {
				out[t] = true
			}

			continue
		}
		// not a named set: a resource family, every tool with that prefix
		found := false
		for _, t := range known {
			if strings.HasPrefix(t, name+"_") {
				out[t], found = true, true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown toolset %q (sets: %s, %s; or a resource family: %s)",
				name, All, strings.Join(r.ToolsetNames(), ", "), strings.Join(families(known), ", "))
		}
	}
	// core is what every other set assumes: without it there is no way to
	// find anything or open it
	for _, t := range r.cfg.Toolsets[Core] {
		out[t] = true
	}

	return out, nil
}

// families lists the resource prefixes in use, sorted.
func families(known []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range known {
		if i := strings.Index(t, "_"); i > 0 && !seen[t[:i]] {
			seen[t[:i]] = true
			out = append(out, t[:i])
		}
	}
	slices.Sort(out)

	return out
}

// compilePatterns expands the essential preset and checks that every other
// pattern matches at least one tool.
func (r *Registry) compilePatterns(raw, known []string, which string) ([]string, error) {
	var out []string
	for _, pat := range entries(raw) {
		if pat == Essential {
			out = append(out, r.cfg.Essential...)

			continue
		}
		if !slices.ContainsFunc(known, func(n string) bool { return matchPattern(pat, n) }) {
			return nil, fmt.Errorf("%s-tools pattern %q matches no tool (have: %s)", which, pat, strings.Join(known, ", "))
		}
		out = append(out, pat)
	}

	return out, nil
}

func matchesAny(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return matchPattern(p, name) })
}

// matchPattern takes an exact name, or one with a single "*" at its start
// or its end.
func matchPattern(pattern, name string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(name, strings.TrimPrefix(pattern, "*"))
	default:
		return pattern == name
	}
}
