// Package acctest drives an MCP server the way a client does, for a suite
// that runs against a live one: calls that count what they covered, readers
// for the decoded answers, waits on what a server does in the background, and
// put-backs for what a test changed.
//
// Nothing here knows an app's tools or fixtures. What a suite needs of the
// server under test itself - its environment, the proxy its lookups go
// through, files laid out where it reads them - is in test/env.
package acctest

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Suite is one client session to the server under test, and the record of
// what it was asked.
type Suite struct {
	// Ctx is the run's own context, not a test's: a test's is cancelled
	// before its clean-ups run, which is when a put-back calls
	Ctx     context.Context //nolint:containedctx // the run's own context, which every call the suite makes is under
	Session *mcp.ClientSession
	// Ready says the server is there to be called; a test that calls when it
	// is not skips, saying NotReady
	Ready    bool
	NotReady string
	// CannotAnswer are the tools a throwaway server gives nothing to answer
	// with, so a call that fails is the only one a test can make, and why
	CannotAnswer map[string]string

	mu     sync.Mutex
	called map[string]Calls
}

// Connect serves an MCP server in this process and returns a suite holding a
// client session to it, ready to be called: the tools are driven as a client
// drives them, with nothing between but the protocol.
func Connect(ctx context.Context, server *mcp.Server) (*Suite, error) {
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		return nil, err
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "acctest", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}

	return &Suite{Ctx: ctx, Session: session, Ready: true}, nil
}

// Calls is how a tool's calls through Invoke went: the ones that answered,
// and the ones that failed - a refusal the test asked for, or an error it did
// not.
type Calls struct {
	Answered, Failed int
}

// Calls is how a tool's calls have gone so far.
func (s *Suite) Calls(name string) Calls {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.called[name]
}

// Invoke calls a tool and returns its structured result. Every tool call in
// a suite comes through here, so this is also where coverage is recorded: an
// answer and a failure are counted apart (see Uncovered).
func (s *Suite) Invoke(name string, args map[string]any) (map[string]any, error) {
	out, err := s.callTool(name, args)

	s.mu.Lock()
	if s.called == nil {
		s.called = map[string]Calls{}
	}
	c := s.called[name]
	if err != nil {
		c.Failed++
	} else {
		c.Answered++
	}
	s.called[name] = c
	s.mu.Unlock()

	return out, err
}

func (s *Suite) callTool(name string, args map[string]any) (map[string]any, error) {
	if args == nil {
		args = map[string]any{}
	}
	res, err := s.Session.CallTool(s.Ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}

		return nil, fmt.Errorf("%s: %s", name, strings.Join(msgs, "; "))
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: structured content is %T", name, res.StructuredContent)
	}

	return out, nil
}

// Call invokes a tool, skipping the test when the server is not there and
// failing it when the tool errors.
func (s *Suite) Call(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()

	if !s.Ready {
		t.Skip(s.NotReady)
	}
	out, err := s.Invoke(name, args)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// CallErr invokes a tool expecting it to fail, and returns the error message.
func (s *Suite) CallErr(t *testing.T, name string, args map[string]any) string {
	t.Helper()

	if !s.Ready {
		t.Skip(s.NotReady)
	}
	out, err := s.Invoke(name, args)
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded: %v", name, out)
	}

	return err.Error()
}

// ToolNames lists every tool the server registered, so a test can assert
// that a family is complete rather than only that the tools it knows about
// work.
func (s *Suite) ToolNames(t *testing.T) []string {
	t.Helper()

	if !s.Ready {
		t.Skip(s.NotReady)
	}
	res, err := s.Session.ListTools(s.Ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}

	return out
}

// Uncovered names the registered tools no test has seen answer: those never
// called at all, and those whose every call failed. A tool that is only
// listed is not tested, and nor is one only ever refused - that proves it
// checks what it is given, not that it does its job - so adding a tool
// without a test of it working fails the suite rather than quietly widening
// the untested surface. The few that cannot answer (CannotAnswer) need a
// call all the same.
func (s *Suite) Uncovered() (never, onlyFailed []string, err error) {
	res, err := s.Session.ListTools(s.Ctx, nil)
	if err != nil {
		return nil, nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, tool := range res.Tools {
		c := s.called[tool.Name]
		switch {
		case c.Answered > 0:
		case c.Failed > 0 && s.CannotAnswer[tool.Name] != "":
		case c.Failed > 0:
			onlyFailed = append(onlyFailed, tool.Name)
		default:
			never = append(never, tool.Name)
		}
	}
	slices.Sort(never)
	slices.Sort(onlyFailed)

	return never, onlyFailed, nil
}

// CoverageReport is Uncovered as a suite's TestMain prints it once the tests
// have run: "" when every registered tool answered something, and otherwise
// the tools that were never called and the ones that only ever failed. A run
// that got "" back with a nil error may pass. Only a whole run can say a tool
// was never called (WholeRun).
func (s *Suite) CoverageReport() (string, error) {
	never, onlyFailed, err := s.Uncovered()
	if err != nil {
		return "", fmt.Errorf("tool coverage: could not list tools: %w", err)
	}
	if len(never)+len(onlyFailed) == 0 {
		return "", nil
	}

	var b strings.Builder
	if len(never) > 0 {
		fmt.Fprintf(&b, "\n%d registered tool(s) are never called by this suite:\n", len(never))
		for _, name := range never {
			fmt.Fprintln(&b, "  "+name)
		}
	}
	if len(onlyFailed) > 0 {
		fmt.Fprintf(&b, "\n%d registered tool(s) only ever failed in this suite, so nothing shows they work:\n", len(onlyFailed))
		for _, name := range onlyFailed {
			fmt.Fprintf(&b, "  %s (%d failed calls)\n", name, s.Calls(name).Failed)
		}
	}
	fmt.Fprintln(&b, "every tool needs a test that it answers; add one or remove the tool")

	return b.String(), nil
}

// WholeRun reports whether this test binary was asked for every test: with a
// -run or -skip filter the tools the filtered-out tests call go uncalled,
// which says nothing about coverage.
func WholeRun() bool {
	return unfiltered(func(name string) string {
		if f := flag.Lookup(name); f != nil {
			return f.Value.String()
		}

		return ""
	})
}

// unfiltered reports whether neither flag that narrows a run is set.
func unfiltered(flagValue func(name string) string) bool {
	return flagValue("test.run") == "" && flagValue("test.skip") == ""
}

// retryWaits is how long Retried waits before each try: backing off, for
// about a minute in all. One server refused to remove a library for longer
// than ten seconds after a scan.
var retryWaits = []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second}

// Retried calls a tool until it succeeds, backing off for about a minute in
// all: a server can refuse a write while work of its own holds the record. It
// returns the last error.
func (s *Suite) Retried(tool string, args map[string]any) error {
	return s.retryWith(retryWaits, tool, args)
}

func (s *Suite) retryWith(waits []time.Duration, tool string, args map[string]any) error {
	var err error
	for _, wait := range waits {
		time.Sleep(wait)
		if _, err = s.Invoke(tool, args); err == nil {
			return nil
		}
	}

	return err
}

// PutBack calls a tool once the test ends, to undo what the test changed,
// and reports one that never succeeds rather than leaving the change for the
// tests after.
func (s *Suite) PutBack(t *testing.T, tool string, args map[string]any) {
	t.Helper()

	t.Cleanup(func() { s.Undo(t, tool, args) })
}

// Undo is PutBack for a call made in a clean-up already under way.
func (s *Suite) Undo(t *testing.T, tool string, args map[string]any) {
	t.Helper()

	if err := s.Retried(tool, args); err != nil {
		t.Errorf("putting back with %s %v: %v", tool, args, err)
	}
}

// DeleteLaterIfThere deletes what a test made once it ends, unless the test
// deleted it itself: an answer that it is gone (gone, e.g. "no collection
// named") is no failure. Any other is reported, as PutBack does.
func (s *Suite) DeleteLaterIfThere(t *testing.T, tool string, args map[string]any, gone string) {
	t.Helper()

	t.Cleanup(func() {
		if _, err := s.Invoke(tool, args); err == nil || strings.Contains(err.Error(), gone) {
			return
		}
		s.Undo(t, tool, args)
	})
}
