//go:build integration

// The harness: scripts/testenv.sh creates the container and exports
// SONARR_SERVER, SONARR_TOKEN and SONARR_TEST_*; this starts the provider
// proxy and the fake indexer and download client, then the container, then
// drives Sonarr through the MCP tools rather than the HTTP API, so building
// the fixtures is itself a test of rootfolder_add, series_import, series_add
// and the rest.
package acceptance

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/sonarr-mcp/internal/cassettes"
	"github.com/katbyte/sonarr-mcp/lib/providerproxy"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/katbyte/sonarr-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	ctx     context.Context
	session *mcp.ClientSession
	// api is the SDK straight onto the same Sonarr, for what the tools do not
	// cover (custom formats, the indexer and client settings) and for
	// checking what a tool did from the other side
	api   *sonarr.Client
	ready bool
	proxy *providerproxy.Proxy
)

// recording reports whether this run should call the real providers and
// refresh the cassettes, rather than replay them.
func recording() bool { return os.Getenv("SONARR_TEST_RECORD") != "" }

// verifying reports whether to check the cassettes against the live providers
// without rewriting them.
func verifying() bool { return os.Getenv("SONARR_TEST_VERIFY") != "" }

// configured reports whether the container environment is present.
func configured() bool {
	return os.Getenv("SONARR_SERVER") != "" && os.Getenv("SONARR_TOKEN") != "" && os.Getenv("SONARR_TEST_CONTAINER") != ""
}

// dataDir is the host directory the container's /tv, /downloads and /config
// are bind-mounted from, so a test can add or remove files on disk.
func dataDir() string { return os.Getenv("SONARR_TEST_DATA") }

// tvDir is the host side of the container's /tv.
func tvDir() string { return filepath.Join(dataDir(), "tv") }

// envPort reads a port from the environment, with its default.
func envPort(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", name, v, err)
	}

	return n, nil
}

// containerHost is how the container reaches this process.
func containerHost() string {
	if h := os.Getenv("SONARR_TEST_HOST"); h != "" {
		return h
	}

	return "host.docker.internal"
}

// testMain starts the proxy, the fakes and the container, seeds the library,
// and runs.
func testMain(m *testing.M) {
	if !configured() {
		os.Exit(m.Run()) // every test skips
	}
	if err := startProxy(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		os.Exit(1)
	}
	if err := startFakes(); err != nil {
		stopAll()
		fmt.Fprintln(os.Stderr, "fakes:", err)
		os.Exit(1)
	}
	if err := start(); err != nil {
		stopAll()
		fmt.Fprintln(os.Stderr, "acceptance setup:", err)
		os.Exit(1)
	}

	code := m.Run()
	stopAll()

	// a replay miss means a test ran against a 502 rather than a recording, so
	// say so loudly even when the assertions happened to survive it
	if misses := proxyMisses; len(misses) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d request(s) had no recording:\n", len(misses))
		for _, m := range misses {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
		fmt.Fprintln(os.Stderr, "run `make record` to capture them")
		if code == 0 {
			code = 1
		}
	}

	// every registered tool must have been called by something above. Only a
	// whole-suite run can say that, so a -run filter skips the check.
	if f := flag.Lookup("test.run"); f == nil || f.Value.String() == "" {
		missing, err := uncovered()
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "\ntool coverage: could not list tools:", err)
			code = 1
		case len(missing) > 0:
			fmt.Fprintf(os.Stderr, "\n%d registered tool(s) are never called by this suite:\n", len(missing))
			for _, name := range missing {
				fmt.Fprintln(os.Stderr, "  "+name)
			}
			fmt.Fprintln(os.Stderr, "every tool needs a test; add one or remove the tool")
			code = 1
		}

		// and every kind of finding an audit can report must have been
		// reported, so no branch of an audit goes untested
		missing, stale := unreportedFindings()
		if len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "\n%d kind(s) of finding are never reported by this suite:\n", len(missing))
			for _, kind := range missing {
				fmt.Fprintln(os.Stderr, "  "+kind)
			}
			fmt.Fprintln(os.Stderr, "seed the library so the audit reports it, or name it in notReported with the reason")
			code = 1
		}
		if len(stale) > 0 {
			fmt.Fprintf(os.Stderr, "\n%d kind(s) in notReported were reported after all:\n", len(stale))
			for _, kind := range stale {
				fmt.Fprintln(os.Stderr, "  "+kind)
			}
			code = 1
		}
	}

	// drift is only collected under SONARR_TEST_VERIFY: the providers still
	// answer, but no longer in the shape Sonarr decodes
	if drifts := proxyDrifts; len(drifts) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d response(s) changed shape since recording:\n", len(drifts))
		for _, d := range drifts {
			fmt.Fprintln(os.Stderr, "  "+d.String())
		}
		fmt.Fprintln(os.Stderr, "\nreview the changes, then run `make record` to accept them")
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

var (
	proxyMisses []string
	proxyDrifts []providerproxy.Drift
)

func stopAll() {
	removeBinary()
	stopFakes()
	if proxy == nil {
		return
	}
	proxyMisses = proxy.Misses()
	proxyDrifts = proxy.Drifts()
	if err := proxy.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy close:", err)
	}
	proxy = nil
}

var (
	calledMu sync.Mutex
	called   = map[string]bool{}
)

// uncovered names the registered tools no test called. A tool that is only
// listed is not tested, so adding one without a test fails the suite rather
// than quietly widening the untested surface.
func uncovered() ([]string, error) {
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}

	calledMu.Lock()
	defer calledMu.Unlock()

	var missing []string
	for _, tool := range res.Tools {
		if !called[tool.Name] {
			missing = append(missing, tool.Name)
		}
	}
	slices.Sort(missing)

	return missing, nil
}

// startProxy brings up the record/replay proxy the container's HTTPS_PROXY
// points at, signing with the CA scripts/testenv.sh minted and mounted into
// the container.
func startProxy() error {
	port, err := envPort("SONARR_TEST_PROXY_PORT", 18080)
	if err != nil {
		return err
	}
	mode := providerproxy.Replay
	switch {
	case recording():
		mode = providerproxy.Record
	case verifying():
		mode = providerproxy.Verify
	}

	opts := providerproxy.Options{
		Mode:        mode,
		CassetteDir: filepath.Join("testdata", "cassettes"),
		// every interface and both stacks: the container reaches this through
		// host.docker.internal, and a runner that hands the container an IPv6
		// route as well would find nothing on an IPv4-only socket
		Addr: ":" + strconv.Itoa(port),
		// services.sonarr.tv is asked with the machine's architecture, which is
		// Arm64 on a Mac and X64 on a runner: a cassette keyed on it would only
		// replay where it was recorded
		RedactQuery: []string{"arch"},
		// the server reaching itself is not provider traffic, and nor is the
		// probe checkReachable sends to the proxy's own address
		IgnoreHosts: append(containerAddresses(), containerHost()),
		Trim:        cassettes.ForSeries(tvdbIDs(everySeries)...),
	}
	if ca := os.Getenv("SONARR_TEST_PROXY_CA"); ca != "" {
		opts.CACert, opts.CAKey = filepath.Join(ca, "ca.pem"), filepath.Join(ca, "ca.key")
	}
	p, err := providerproxy.New(opts)
	if err != nil {
		return err
	}
	proxy = p

	return nil
}

// start starts the container once the proxy is listening, waits for Sonarr,
// connects an MCP session with every tool registered, and seeds the library.
func start() error {
	name := os.Getenv("SONARR_TEST_CONTAINER")
	if out, err := exec.Command("docker", "start", name).CombinedOutput(); err != nil { //nolint:gosec // the test container's own name
		return fmt.Errorf("docker start %s: %w: %s", name, err, out)
	}
	if err := checkReachable(name); err != nil {
		return err
	}

	c, err := sonarr.New(os.Getenv("SONARR_SERVER"), os.Getenv("SONARR_TOKEN"))
	if err != nil {
		return err
	}
	api = c
	ctx = context.Background()
	if err := waitForSonarr(); err != nil {
		return err
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "sonarr-mcp", Version: "test"}, nil)
	if _, err := tools.RegisterAll(srv, api, tools.Options{EnableDelete: true}); err != nil {
		return err
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		return err
	}
	if session, err = mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil); err != nil {
		return err
	}
	ready = true

	return seed()
}

// waitForSonarr polls the status endpoint until Sonarr answers with the key.
func waitForSonarr() error {
	var last error
	for range 90 {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := api.GetSystemStatus(cctx)
		cancel()
		if err == nil {
			return nil
		}
		last = err
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("sonarr never answered: %w", last)
}

// invoke calls a tool and returns its structured result. Every tool call in
// the suite comes through here, so this is also where coverage is recorded.
func invoke(name string, args map[string]any) (map[string]any, error) {
	calledMu.Lock()
	called[name] = true
	calledMu.Unlock()

	if args == nil {
		args = map[string]any{}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
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
	recordFindings(name, out)

	return out, nil
}

// skipUnlessReady skips a test when there is no container to run against.
func skipUnlessReady(t *testing.T) {
	t.Helper()

	if !ready {
		t.Skip("SONARR_SERVER, SONARR_TOKEN and SONARR_TEST_CONTAINER are not set; run: eval \"$(scripts/testenv.sh up)\"")
	}
}

// call invokes a tool, skipping the test when the container is not configured
// and failing it when the tool errors.
func call(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()

	skipUnlessReady(t)
	out, err := invoke(name, args)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// callErr invokes a tool expecting it to fail, and returns the error message.
func callErr(t *testing.T, name string, args map[string]any) string {
	t.Helper()

	skipUnlessReady(t)
	out, err := invoke(name, args)
	if err == nil {
		t.Fatalf("%s unexpectedly succeeded: %v", name, out)
	}

	return err.Error()
}

// eventually calls a tool until check is satisfied with its answer, for the
// work Sonarr does in the background: a refresh, a scan, an import.
func eventually(t *testing.T, name string, args map[string]any, what string, check func(map[string]any) bool) map[string]any {
	t.Helper()

	var out map[string]any
	for range 60 {
		out = call(t, name, args)
		if check(out) {
			return out
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s never showed %s: %v", name, what, out)

	return nil
}

// rows pulls a list of objects out of a decoded JSON field.
func rows(t *testing.T, v any, field string) []map[string]any {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", field, v)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		row, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("%s contains %T, want objects", field, e)
		}
		out = append(out, row)
	}

	return out
}

// rowsOf pulls a list of objects out of a decoded JSON field, tolerating a
// missing one.
func rowsOf(v any) []map[string]any {
	raw, _ := v.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		if row, ok := e.(map[string]any); ok {
			out = append(out, row)
		}
	}

	return out
}

// strs pulls a []string out of a decoded JSON field.
func strs(t *testing.T, v any, field string) []string {
	t.Helper()

	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%s is %T, want a list", field, v)
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			t.Fatalf("%s contains %T, want strings", field, e)
		}
		out = append(out, s)
	}

	return out
}

// num pulls a JSON number out of a decoded field.
func num(t *testing.T, v any, field string) int {
	t.Helper()

	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s is %T (%v), want a number", field, v, v)
	}

	return int(f)
}

// numOr0 pulls a JSON number out of a decoded field, 0 when it was omitted.
func numOr0(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// str pulls a string out of a decoded field, "" when absent.
func str(v any) string {
	s, _ := v.(string)
	return s
}

// object reads a nested object.
func object(t *testing.T, v any, field string) map[string]any {
	t.Helper()

	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T (%v), want an object", field, v, v)
	}

	return m
}

// findRow returns the first row whose field equals value, failing when none
// does.
func findRow(t *testing.T, list []map[string]any, field, value string) map[string]any {
	t.Helper()

	for _, row := range list {
		if str(row[field]) == value {
			return row
		}
	}
	t.Fatalf("no row with %s %q in %v", field, value, list)

	return nil
}

// findings are an audit's findings, those matching every filter given as
// field/value pairs.
func findings(t *testing.T, out map[string]any, filter ...string) []map[string]any {
	t.Helper()

	var match []map[string]any
	for _, f := range rows(t, out["findings"], "findings") {
		ok := true
		for i := 0; i+1 < len(filter); i += 2 {
			if !strings.Contains(str(f[filter[i]]), filter[i+1]) {
				ok = false
			}
		}
		if ok {
			match = append(match, f)
		}
	}

	return match
}

// containerAddresses are the addresses the container reaches itself on,
// which the proxy answers without a cassette (Options.IgnoreHosts).
func containerAddresses() []string {
	name := os.Getenv("SONARR_TEST_CONTAINER")
	if name == "" {
		return nil
	}
	out, err := exec.Command("docker", "inspect", "-f", //nolint:gosec // the test container's own name
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}{{.Config.Hostname}}", name).Output()
	if err != nil {
		return nil
	}

	return strings.Fields(string(out))
}

// checkReachable proves, from inside the container, that Sonarr can reach
// this process: the proxy, and the fake indexer and download client. One that
// cannot fails every lookup, search and download with a timeout of its own,
// which reads as dozens of unrelated failures rather than the one plumbing
// problem it is - so say it plainly, once, before the suite runs.
func checkReachable(name string) error {
	var ports []string
	for _, env := range []string{"SONARR_TEST_PROXY_PORT", "SONARR_TEST_INDEXER_PORT", "SONARR_TEST_SAB_PORT"} {
		if p := os.Getenv(env); p != "" {
			ports = append(ports, p)
		}
	}
	var errs []error
	for range 30 {
		errs = errs[:0]
		for _, port := range ports {
			script := fmt.Sprintf("curl -s -o /dev/null -m 3 http://%s:%s/ || exit 1", containerHost(), port)
			if out, err := exec.Command("docker", "exec", name, "sh", "-c", script).CombinedOutput(); err != nil { //nolint:gosec // the test container's own name
				errs = append(errs, fmt.Errorf("port %s: %w: %s", port, err, out))
			}
		}
		if len(errs) == 0 {
			return nil
		}
		time.Sleep(time.Second)
	}

	return fmt.Errorf("%s cannot reach this process on %s: %w", name, containerHost(), errors.Join(errs...))
}

// waitHTTP polls a URL until it answers, for a fake that has just started.
func waitHTTP(url string) error {
	var last error
	for range 50 {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}

	return last
}

// itoa is strconv.Itoa, for building references like tvdb:280619.
func itoa(n int) string { return strconv.Itoa(n) }
