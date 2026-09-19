package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tools end to end against a canned Sonarr: the requests a handler builds
// and the answer it projects, without a container. The live suite proves the
// canned shapes match a real Sonarr; these pin the behaviour its fixtures
// cannot reach - a grab gone quiet for six hours, a root folder short of
// space, an ambiguous title - and the edges of the pure functions.

// fakeServer is a canned Sonarr API: routes on a ServeMux plus a record of
// every request the tools made to it.
type fakeServer struct {
	mux *http.ServeMux
	srv *httptest.Server

	mu     sync.Mutex
	seen   []request
	bodies map[string][]string
}

type request struct {
	Method, Path, Query string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()

	f := &fakeServer{mux: http.NewServeMux(), bodies: map[string][]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, request{r.Method, r.URL.Path, r.URL.RawQuery})
		f.bodies[r.Method+" "+r.URL.Path] = append(f.bodies[r.Method+" "+r.URL.Path], string(body))
		f.mu.Unlock()
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)

	return f
}

// requests returns the calls made to a path, in order.
func (f *fakeServer) requests(path string) []request {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []request
	for _, r := range f.seen {
		if r.Path == path {
			out = append(out, r)
		}
	}

	return out
}

// sent returns the bodies sent to a method and path, in order.
func (f *fakeServer) sent(method, path string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.bodies[method+" "+path]...)
}

// answer serves a fixed JSON value on a route.
func (f *fakeServer) answer(t *testing.T, pattern string, v any) {
	t.Helper()

	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, v) })
}

// answerStatus serves a fixed JSON value with a status on a route.
func (f *fakeServer) answerStatus(t *testing.T, pattern string, status int, v any) {
	t.Helper()

	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Error(err)
		}
	})
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

// session connects an in-memory MCP client to a server registering every
// tool against the fake, with the given options.
func session(t *testing.T, f *fakeServer, opts Options) *mcp.ClientSession {
	t.Helper()

	client, err := sonarr.New(f.srv.URL, "k")
	if err != nil {
		t.Fatal(err)
	}
	client.Client.HTTPClient = f.srv.Client()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	opts.Toolsets = []string{"all"}
	if _, err := RegisterAll(srv, client, opts); err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// callTool calls a tool and returns its structured result, or its error text.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (out map[string]any, errText string) {
	t.Helper()

	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		var msgs []string
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				msgs = append(msgs, tc.Text)
			}
		}
		return nil, strings.Join(msgs, "; ")
	}
	out, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("%s: structured content is %T", name, res.StructuredContent)
	}

	return out, ""
}

// mustCall calls a tool that must succeed.
func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()

	out, errText := callTool(t, cs, name, args)
	if errText != "" {
		t.Fatalf("%s: %s", name, errText)
	}

	return out
}

// mustFail calls a tool that must fail, and returns why.
func mustFail(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()

	out, errText := callTool(t, cs, name, args)
	if errText == "" {
		t.Fatalf("%s unexpectedly succeeded: %v", name, out)
	}

	return errText
}

// rowsOf pulls a list of objects out of a decoded JSON field.
func rowsOf(v any) []map[string]any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if row, ok := e.(map[string]any); ok {
			out = append(out, row)
		}
	}

	return out
}

// text is a decoded JSON string, or "" for anything else.
func text(v any) string {
	if s, ok := v.(string); ok {
		return s
	}

	return ""
}

// number is a decoded JSON number, or 0 for anything else.
func number(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}

	return 0
}

// flag is a decoded JSON boolean, false for anything else.
func flag(v any) bool {
	b, ok := v.(bool)

	return ok && b
}
