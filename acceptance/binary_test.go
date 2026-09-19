//go:build integration

// The binary, not the package. Everything else in this suite drives
// tools.RegisterAll in process, which is every line of tool code the binary
// runs; what it never touches is the thin layer around it - stdio framing,
// the flags and environment reaching the server, the HTTP routes, and what a
// bad start says. Those break silently: a stray line on stdout corrupts the
// protocol and a client simply fails to connect, with every other test green.
//
// So this is a smoke test: the real binary, built from this checkout, spoken
// to the way a client speaks to it, against the same seeded Sonarr.
package acceptance

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	binOnce sync.Once
	binDir  string
	binPath string
	binErr  error
)

// removeBinary deletes the binary the smoke test built, at the end of the
// run: it is built once for the suite, so no one test can clean it up.
func removeBinary() {
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
}

// binary builds sonarr-mcp from this checkout, once per run, so the test
// speaks to the code in the tree rather than whatever is installed.
func binary(t *testing.T) string {
	t.Helper()

	skipUnlessReady(t)
	binOnce.Do(func() {
		if binDir, binErr = os.MkdirTemp("", "sonarr-mcp-binary"); binErr != nil {
			return
		}
		binPath = filepath.Join(binDir, "sonarr-mcp")
		if out, err := exec.CommandContext(ctx, "go", "build", "-o", binPath, "..").CombinedOutput(); err != nil {
			binErr = fmt.Errorf("building sonarr-mcp: %w\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}

	return binPath
}

// binaryEnv is the environment a client would give the binary: the seeded
// Sonarr and its key, and nothing else of ours. HOME is an empty directory,
// so a real ~/.sonarr-mcp cannot change what is registered.
func binaryEnv(t *testing.T, extra ...string) []string {
	t.Helper()

	return append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"SONARR_SERVER=" + os.Getenv("SONARR_SERVER"),
		"SONARR_TOKEN=" + os.Getenv("SONARR_TOKEN"),
	}, extra...)
}

// stdio starts "sonarr-mcp serve" as a client does and connects an MCP
// session over its stdin and stdout.
func stdio(t *testing.T, env []string, args ...string) *mcp.ClientSession {
	t.Helper()

	cmd := exec.CommandContext(context.WithoutCancel(ctx), binary(t), append([]string{"serve"}, args...)...)
	cmd.Env = env
	cmd.Dir = t.TempDir()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "binary-test", Version: "0"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connecting over stdio: %v\nstderr: %s", err, stderr.String())
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

func listNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}

	return names
}

// callSession calls a tool over a session, failing on an error.
func callSession(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s: %v %v", name, err, res)
	}
	out, _ := res.StructuredContent.(map[string]any)

	return out
}

// Over stdio, with only the environment a client sets, the binary registers
// the core set and answers from the seeded Sonarr.
func TestBinaryStdio(t *testing.T) {
	cs := stdio(t, binaryEnv(t))

	names := listNames(t, cs)
	want := []string{"calendar_list", "episode_list", "queue_list", "series_get", "series_list", "server_info"}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Errorf("the default tools = %v, want core %v", names, want)
	}
	info := callSession(t, cs, "server_info", nil)
	if !strings.HasPrefix(str(info["sonarr_version"]), "4.") || numOr0(info["series"]) < len(importedSeed) {
		t.Errorf("server_info over stdio = %v", info)
	}
	if got := callSession(t, cs, "series_get", map[string]any{"series": "tvdb:78874"}); str(got["title"]) != firefly.Title {
		t.Errorf("series_get over stdio = %v", got["title"])
	}
}

// The names the rest of the Sonarr tooling exports reach the binary too, and
// the toolset and read-only flags change what it registers.
func TestBinaryFlagsAndAliases(t *testing.T) {
	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"SONARR_URL=" + os.Getenv("SONARR_SERVER"), "SONARR_API_KEY=" + os.Getenv("SONARR_TOKEN"),
	}
	cs := stdio(t, env, "--toolsets", "curation", "--read-only")

	names := listNames(t, cs)
	if !slices.Contains(names, "audit_all") || !slices.Contains(names, "series_list") {
		t.Errorf("curation lacks its audits or core: %v", names)
	}
	for _, n := range names {
		if slices.Contains([]string{"series_edit", "file_edit", "queue_remove", "import_apply", "series_delete"}, n) {
			t.Errorf("%s is registered under --read-only", n)
		}
	}
	all := callSession(t, cs, "audit_all", map[string]any{"series": chernobyl.Title})
	if numOr0(all["series"]) != 1 {
		t.Errorf("audit_all over stdio = %v", all)
	}
}

// Over HTTP: the health probe answers without a token, the MCP endpoint only
// with it.
func TestBinaryHTTP(t *testing.T) {
	bin := binary(t)
	addr := freeAddr(t)
	const token = "binary-test-token" //nolint:gosec // a test's own bearer token
	cmd := exec.CommandContext(context.WithoutCancel(ctx), bin, "serve", "--listen", addr, "--toolsets", "all")
	cmd.Env = binaryEnv(t, "SONARR_AUTH_TOKEN="+token)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	})
	if err := waitHTTP("http://" + addr + "/healthz"); err != nil {
		t.Fatalf("the HTTP server never answered: %v\n%s", err, stderr.String())
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/mcp", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/mcp without a token = %d, want 401", resp.StatusCode)
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:   "http://" + addr + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token: token}},
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "binary-test", Version: "0"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connecting over HTTP: %v\n%s", err, stderr.String())
	}
	t.Cleanup(func() { _ = cs.Close() })
	if names := listNames(t, cs); !slices.Contains(names, "series_edit") || slices.Contains(names, "series_delete") {
		t.Errorf("--toolsets all without --enable-delete = %d tools: %v", len(names), names)
	}
	if got := callSession(t, cs, "series_list", map[string]any{"query": "chernobyl"}); numOr0(got["total"]) != 1 {
		t.Errorf("series_list over HTTP = %v", got)
	}
}

// bearer adds the bearer token to every request.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)

	return http.DefaultTransport.RoundTrip(r)
}

// freeAddr is a loopback address with a port nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	return addr
}

// A bad start says what is wrong and exits, rather than serving nothing.
func TestBinaryRefusals(t *testing.T) {
	bin := binary(t)
	for _, c := range []struct {
		name string
		args []string
		env  []string
		want string
	}{
		{"no key", []string{"serve"}, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "SONARR_SERVER=http://127.0.0.1:1"}, "token parameter can't be empty"},
		{"an open port", []string{"serve", "--listen", "127.0.0.1:0"}, binaryEnv(t), "--listen needs --auth-token"},
		{"a toolset that is not one", []string{"serve", "--toolsets", "everything"}, binaryEnv(t), "unknown toolset"},
		{"a wrong key", []string{"info"}, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "SONARR_SERVER=" + os.Getenv("SONARR_SERVER"), "SONARR_TOKEN=nope"}, "API key rejected"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(cctx, bin, c.args...)
			cmd.Env = c.env
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), c.want) {
				t.Errorf("%v = %v\n%s", c.args, err, out)
			}
		})
	}
}

// info and tools, the two commands a person runs by hand.
func TestBinaryCommands(t *testing.T) {
	bin := binary(t)

	cmd := exec.CommandContext(ctx, bin, "info")
	cmd.Env = binaryEnv(t)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "root folder /tv") || !strings.Contains(string(out), "4.0.20") {
		t.Errorf("info = %v\n%s", err, out)
	}

	cmd = exec.CommandContext(ctx, bin, "tools", "--toolsets", "all", "--enable-delete")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "series_delete") || !strings.Contains(string(out), "delete") {
		t.Errorf("tools = %v\n%s", err, out)
	}
	// every tool this suite registers is listed, and no more
	for _, name := range toolNames(t) {
		if !strings.Contains(string(out), " "+name+" ") {
			t.Errorf("tools does not list %s", name)
		}
	}
}
