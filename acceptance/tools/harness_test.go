//go:build acceptance

// The harness: .scripts/testenv.sh creates the container and exports
// SONARR_SERVER, SONARR_TOKEN and SONARR_TEST_*; this starts the provider
// proxy and the fake indexer and download client, then the container
// (lib/testenv), then drives Sonarr through the MCP tools rather than the
// HTTP API, so building the fixtures is itself a test of rootfolder_add,
// series_import, series_add and the rest.
package tools_test

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/go-kt/test/services/sabnzbd"
	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/sonarr-mcp/lib/testenv"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
	"github.com/katbyte/sonarr-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	ctx context.Context
	// suite is the MCP session to the server under test, with every tool
	// registered, and the record of what the tests asked of it
	suite = &acctest.Suite{NotReady: testenv.NotConfigured}
	// api is the SDK straight onto the same Sonarr, for what the tools do not
	// cover (custom formats, the indexer and client settings) and for
	// checking what a tool did from the other side
	api   *sonarr.Client
	proxy *testenv.Proxy
	fakes *testenv.Fakes
	// the fake indexer and download client, as the tests name them
	indexer *newznab.Indexer
	sab     *sabnzbd.Server
)

// fakePorts are the ports the fakes listen on when the environment names
// none, as .scripts/testenv.sh defaults them.
var fakePorts = testenv.Ports{Indexer: 18081, SAB: 18082}

// tvDir is the host side of the container's /tv.
func tvDir() string { return testenv.DataDir("tv") }

// runSuite starts the proxy, the fakes and the container, seeds the library,
// runs the tests, and returns the exit code.
func runSuite(m *testing.M) int {
	if !testenv.Configured() {
		return m.Run() // every test skips
	}
	ctx = context.Background()
	if err := startAround(); err != nil {
		stopAll()
		fmt.Fprintln(os.Stderr, "acceptance setup:", err)

		return 1
	}

	code := m.Run()
	stopAll()

	// a replay miss means a test ran against a 502 rather than a recording,
	// and a drift that a provider no longer answers in the shape Sonarr
	// decodes; either fails the run even when the assertions survived it
	if report := proxy.Report(); report != "" {
		fmt.Fprint(os.Stderr, report)
		if code == 0 {
			code = 1
		}
	}

	// no answer and no refusal may hand a caller a credential, whichever test asked for it: Sonarr's API key, the key it keeps for the fake indexer
	// and download client, or one a test gave it
	if leaks := suite.LeakReport(); leaks != "" {
		fmt.Fprint(os.Stderr, leaks)
		code = 1
	}

	// a tool that changes nothing called nothing that changes Sonarr, whichever test called it
	if report := readOnlyReport(); report != "" {
		fmt.Fprint(os.Stderr, report)
		code = 1
	}

	// every registered tool must have been seen to answer by something above.
	// Only a whole run can say that, so a -run or -skip filter skips the check.
	if acctest.WholeRun() {
		report, err := suite.CoverageReport()
		if err != nil {
			fmt.Fprintln(os.Stderr, "\n"+err.Error())
			code = 1
		}
		if report != "" {
			fmt.Fprint(os.Stderr, report)
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

	return code
}

// startAround brings up what Sonarr reaches for as it starts and runs: the
// record/replay proxy, then the fake indexer and download client, then the
// container itself, and seeds the library.
func startAround() error {
	var err error
	if proxy, err = testenv.ListenProxy(ctx, tvdbIDs(everySeries)...); err != nil {
		return fmt.Errorf("provider proxy: %w", err)
	}
	if fakes, err = testenv.StartFakes(fakePorts, catalogue()); err != nil {
		return fmt.Errorf("fakes: %w", err)
	}
	indexer, sab = fakes.Indexer, fakes.SAB

	return start()
}

// stopAll stops the fakes and the proxy, which keeps what it saw.
func stopAll() {
	removeBinary()
	fakes.Stop()
	if err := proxy.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// start starts the container once the proxy is listening, waits for Sonarr,
// connects an MCP session with every tool registered, and seeds the library.
func start() error {
	if err := testenv.StartContainer(ctx, fakes); err != nil {
		return err
	}

	c, err := sonarr.New(testenv.Server(), testenv.Token(), client.WithCalls(&readOnly.calls))
	if err != nil {
		return err
	}
	api = c
	if err := learnReadOnly(); err != nil {
		return err
	}
	if err := waitForSonarr(); err != nil {
		return err
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "sonarr-mcp", Version: "test"}, nil)
	if _, err := tools.RegisterAll(srv, api, tools.Options{EnableDelete: true}); err != nil {
		return err
	}
	connected, err := acctest.Connect(ctx, srv)
	if err != nil {
		return err
	}
	connected.NotReady = testenv.NotConfigured
	suite = connected
	// everything a tool says from here on is read for these, and for whatever a test gives Sonarr through suite.Secret (LeakReport, in runSuite)
	suite.Secret(testenv.FakeKey)
	suite.Secret(testenv.Token())

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
// the suite comes through here: go-kt's suite counts it towards coverage, an
// audit's answer is read for the kinds of finding it reported, and what a
// read-only tool sent Sonarr is checked to change nothing.
func invoke(name string, args map[string]any) (map[string]any, error) {
	defer checkReadOnly(name, sentSoFar())

	out, err := suite.Invoke(name, args)
	if err != nil {
		return nil, err
	}
	recordFindings(name, out)

	return out, nil
}

// skipUnlessReady skips a test when there is no container to run against.
func skipUnlessReady(t *testing.T) {
	t.Helper()

	if !suite.Ready {
		t.Skip(suite.NotReady)
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
func eventually(t *testing.T, name string, args map[string]any, what string, check func(map[string]any) bool) {
	t.Helper()

	var out map[string]any
	for range 60 {
		out = call(t, name, args)
		if check(out) {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s never showed %s: %v", name, what, out)
}

// mergeArgs is a tool's arguments with more put over them.
func mergeArgs(base, more map[string]any) map[string]any {
	out := map[string]any{}
	maps.Copy(out, base)
	maps.Copy(out, more)

	return out
}

// findRow returns the first row whose field equals value, failing when none
// does.
func findRow(t *testing.T, list []map[string]any, field, value string) map[string]any {
	t.Helper()

	for _, row := range list {
		if acctest.Str(row[field]) == value {
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
	for _, f := range acctest.Rows(t, out["findings"], "findings") {
		ok := true
		for i := 0; i+1 < len(filter); i += 2 {
			if !strings.Contains(acctest.Str(f[filter[i]]), filter[i+1]) {
				ok = false
			}
		}
		if ok {
			match = append(match, f)
		}
	}

	return match
}

// itoa is strconv.Itoa, for building references like tvdb:280619.
func itoa(n int) string { return strconv.Itoa(n) }
