//go:build integration

// The harness: scripts/testenv.sh creates the container and exports
// SONARR_SERVER, SONARR_TOKEN and SONARR_TEST_*; runSuite starts the provider
// proxy and the fakes, then the container, builds the SDK client with a
// transport that records every request, seeds the library, runs the suite,
// and checks every operation was called.
//
// What is deliberately not exercised, and why, is in notCalled (coverage.go
// of this package): the operations that restart or stop Sonarr, restore a
// backup over it, or answer only a browser.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/sonarr-mcp/internal/cassettes"
	"github.com/katbyte/sonarr-mcp/internal/fakes/newznab"
	"github.com/katbyte/sonarr-mcp/internal/fakes/sabnzbd"
	"github.com/katbyte/sonarr-mcp/lib/client"
	"github.com/katbyte/sonarr-mcp/lib/providerproxy"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
)

// seriesFixture is a series in the library and what Sonarr should hold for
// it once seeded.
type seriesFixture struct {
	Title  string
	TvdbID int
	// Folder is its folder under /tv; "" for a series added with no files.
	Folder string
	// Files is how many episode files Sonarr should import from the folder.
	Files int
}

// The series, as scripts/testenv.sh lays them out. The suite only reads
// Firefly, so the sweep has a series nothing else changes; Chernobyl's files
// are the ones the file tests edit and delete, Breaking Bad is the one the
// downloads are for, and The Expanse (left out of the seed) is the one the
// series lifecycle imports, moves and deletes.
var (
	firefly     = seriesFixture{Title: "Firefly", TvdbID: 78874, Folder: "Firefly", Files: 6}
	chernobyl   = seriesFixture{Title: "Chernobyl", TvdbID: 360893, Folder: "Chernobyl (2019)", Files: 5}
	cowboyBebop = seriesFixture{Title: "Cowboy Bebop", TvdbID: 76885, Folder: "Cowboy Bebop", Files: 2}
	severance   = seriesFixture{Title: "Severance", TvdbID: 371980, Folder: "Severance", Files: 2}
	breakingBad = seriesFixture{Title: "Breaking Bad", TvdbID: 81189}
	theExpanse  = seriesFixture{Title: "The Expanse", TvdbID: 280619, Folder: "The Expanse", Files: 2}
	seeded      = []seriesFixture{firefly, chernobyl, cowboyBebop, severance}
	// bandOfBrothers is added and deleted by TestSeriesAddAndDelete
	bandOfBrothers = seriesFixture{Title: "Band of Brothers", TvdbID: 74205}
)

// everySeries is every series the suite adds: of the providers' lists of
// every show they know, the recordings keep only these (internal/cassettes).
var everySeries = []seriesFixture{firefly, chernobyl, cowboyBebop, severance, breakingBad, theExpanse, bandOfBrothers}

// tvdbIDs are the series' TheTVDB ids.
func tvdbIDs(series []seriesFixture) []int {
	ids := make([]int, 0, len(series))
	for _, s := range series {
		ids = append(ids, s.TvdbID)
	}

	return ids
}

// profileName is the quality profile the seeded series are on.
const profileName = "HD-1080p"

// fakeKey is the API key both fakes demand, so a Sonarr that sent none would
// be caught.
const fakeKey = "sonarr-mcp-fake"

var (
	sc  *sonarr.Client
	rec *recorder

	proxy       *providerproxy.Proxy
	proxyMisses []string
	proxyDrifts []providerproxy.Drift

	indexer *newznab.Server
	sab     *sabnzbd.Server

	// what the seed made, for the tests to find
	seriesIDs      = map[string]int{}
	rootFolderID   int
	profileID      int
	indexerID      int
	downloadClient int
)

// recording reports whether this run should call the real providers and
// refresh the cassettes, rather than replay them.
func recording() bool { return os.Getenv("SONARR_TEST_RECORD") != "" }

// verifying reports whether to check the cassettes against the live
// providers without rewriting them.
func verifying() bool { return os.Getenv("SONARR_TEST_VERIFY") != "" }

// configured reports whether the container environment is present.
func configured() bool {
	return os.Getenv("SONARR_SERVER") != "" && os.Getenv("SONARR_TOKEN") != "" && os.Getenv("SONARR_TEST_CONTAINER") != ""
}

// dataDir is the host directory the container's /tv, /downloads and /config
// are bind-mounted from.
func dataDir() string { return os.Getenv("SONARR_TEST_DATA") }

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

// runSuite starts everything, seeds the library, runs the tests, and returns
// the exit code.
func runSuite(m *testing.M) int {
	if !configured() {
		return m.Run() // every test skips
	}
	if err := startProxy(); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		return 1
	}
	defer stopAll()
	if err := startFakes(); err != nil {
		fmt.Fprintln(os.Stderr, "fakes:", err)
		return 1
	}
	ctx := context.Background()
	if err := startSonarr(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "sonarr:", err)
		return 1
	}
	if err := seed(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		return 1
	}

	code := m.Run()
	stopAll()

	// a replay miss means a test ran against a 502 rather than a recording,
	// so say so loudly even when the assertions happened to survive it
	if len(proxyMisses) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d request(s) had no recording:\n", len(proxyMisses))
		for _, m := range proxyMisses {
			fmt.Fprintln(os.Stderr, "  "+m)
		}
		fmt.Fprintln(os.Stderr, "run `make record` to capture them")
		if code == 0 {
			code = 1
		}
	}
	// drift is only collected under SONARR_TEST_VERIFY: the providers still
	// answer, but no longer in the shape Sonarr decodes
	if len(proxyDrifts) > 0 {
		fmt.Fprintf(os.Stderr, "\nprovider proxy: %d response(s) changed shape since recording:\n", len(proxyDrifts))
		for _, d := range proxyDrifts {
			fmt.Fprintln(os.Stderr, "  "+d.String())
		}
		fmt.Fprintln(os.Stderr, "\nreview the changes, then run `make record` to accept them")
		if code == 0 {
			code = 1
		}
	}
	// every operation must have been called by something above, or be
	// named in notCalled. Only a whole-suite run can say that.
	if f := flag.Lookup("test.run"); f == nil || f.Value.String() == "" {
		c, err := operationCoverage()
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "\noperation coverage:", err)
			code = 1
		case len(c.missing) > 0 || len(c.stale) > 0:
			if len(c.missing) > 0 {
				fmt.Fprintf(os.Stderr, "\n%d operation(s) are never called by this suite:\n", len(c.missing))
				for _, name := range c.missing {
					fmt.Fprintln(os.Stderr, "  "+name)
				}
				fmt.Fprintln(os.Stderr, "call it from a test, or name it in notCalled with the reason")
			}
			if len(c.stale) > 0 {
				fmt.Fprintf(os.Stderr, "\n%d operation(s) in notCalled are called after all; take them out:\n", len(c.stale))
				for _, name := range c.stale {
					fmt.Fprintln(os.Stderr, "  "+name)
				}
			}
			code = 1
		default:
			fmt.Fprintf(os.Stderr, "\noperation coverage: %d of %d operations called; the other %d are in notCalled\n", c.called, c.total, c.total-c.called)
		}
	}

	return code
}

// stopAll stops the fakes and the proxy, keeping what the proxy saw.
func stopAll() {
	for _, c := range []interface{ Close() error }{indexer, sab} {
		if c != nil && !isNilCloser(c) {
			_ = c.Close()
		}
	}
	indexer, sab = nil, nil
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

// isNilCloser reports whether an interface holds a nil pointer, which a
// fake that never started leaves behind.
func isNilCloser(c interface{ Close() error }) bool {
	switch v := c.(type) {
	case *newznab.Server:
		return v == nil
	case *sabnzbd.Server:
		return v == nil
	default:
		return false
	}
}

// startProxy brings up the record/replay proxy the container's HTTPS_PROXY
// points at, signing with the CA scripts/testenv.sh minted and mounted into
// the container.
func startProxy() error {
	port, err := envPort("SONARR_TEST_PROXY_PORT", 18180)
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
		// host.docker.internal
		Addr: ":" + strconv.Itoa(port),
		// services.sonarr.tv is asked with the machine's architecture, Arm64
		// on a Mac and X64 on a runner: a cassette keyed on it would only
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

// startFakes starts the indexer and the download client, on the ports
// testenv.sh told the container about.
func startFakes() error {
	iport, err := envPort("SONARR_TEST_INDEXER_PORT", 18181)
	if err != nil {
		return err
	}
	sport, err := envPort("SONARR_TEST_SAB_PORT", 18182)
	if err != nil {
		return err
	}
	if indexer, err = newznab.New(newznab.Options{
		Addr: ":" + strconv.Itoa(iport), APIKey: fakeKey, PublicHost: containerHost(), Releases: catalogue(),
	}); err != nil {
		return err
	}
	if sab, err = sabnzbd.New(sabnzbd.Options{
		Addr: ":" + strconv.Itoa(sport), APIKey: fakeKey, PublicHost: containerHost(),
		CompleteDir: "/downloads/complete", HostCompleteDir: filepath.Join(dataDir(), "downloads", "complete"),
	}); err != nil {
		return err
	}
	if err := waitHTTP(indexer.LocalURL() + "/api?t=caps"); err != nil {
		return fmt.Errorf("the fake indexer never answered: %w", err)
	}

	return waitHTTP(sab.LocalURL() + "/api?mode=version")
}

// startSonarr starts the container once the proxy and fakes are listening,
// checks it can reach them, builds the client, and waits for Sonarr.
func startSonarr(ctx context.Context) error {
	name := os.Getenv("SONARR_TEST_CONTAINER")
	if out, err := exec.CommandContext(ctx, "docker", "start", name).CombinedOutput(); err != nil { //nolint:gosec // the test container's own name
		return fmt.Errorf("docker start %s: %w: %s", name, err, out)
	}
	if err := checkReachable(ctx, name); err != nil {
		return err
	}

	c, err := sonarr.New(os.Getenv("SONARR_SERVER"), os.Getenv("SONARR_TOKEN"))
	if err != nil {
		return err
	}
	rec = &recorder{base: http.DefaultTransport}
	c.Client.HTTPClient = &http.Client{Timeout: 2 * time.Minute, Transport: rec}
	sc = c

	var last error
	for range 90 {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := sc.GetSystemStatus(attempt)
		cancel()
		if err == nil {
			return nil
		}
		last = err
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("sonarr never answered: %w", last)
}

// containerAddresses are the addresses the container reaches itself on,
// which the proxy answers without a cassette (Options.IgnoreHosts).
func containerAddresses() []string {
	name := os.Getenv("SONARR_TEST_CONTAINER")
	if name == "" {
		return nil
	}
	out, err := exec.CommandContext(context.Background(), "docker", "inspect", "-f", //nolint:gosec // the test container's own name
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}{{.Config.Hostname}}", name).Output()
	if err != nil {
		return nil
	}

	return strings.Fields(string(out))
}

// checkReachable proves, from inside the container, that Sonarr can reach
// this process: the proxy, and the fake indexer and download client. One
// that cannot fails every lookup, search and download with a timeout of its
// own, which reads as dozens of unrelated failures rather than the one
// plumbing problem it is - so say it plainly, once, before the suite runs.
func checkReachable(ctx context.Context, name string) error {
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
			if out, err := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", script).CombinedOutput(); err != nil { //nolint:gosec // the test container's own name
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

// skipUnlessUp skips a test when there is no container to run against, and
// returns the test's context.
func skipUnlessUp(t *testing.T) context.Context {
	t.Helper()

	if sc == nil {
		t.Skip("SONARR_SERVER, SONARR_TOKEN and SONARR_TEST_CONTAINER are not set; see the package doc")
	}

	return t.Context()
}

// must unwraps a call that must not fail. Go only allows a multi-value call
// as a function's sole argument, so this cannot also take *testing.T - it
// panics instead, which the test framework reports as a failure. That is the
// right severity here: if Sonarr will not answer, nothing downstream is
// meaningful.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}

// status checks the status an operation answered with. The client already
// errors on a status the definitions do not expect, so this is for the
// operations that expect one of several, and to say in the test which one a
// create or an update answers.
func status(t *testing.T, resp *http.Response, want int) {
	t.Helper()

	if resp == nil || resp.StatusCode != want {
		got := 0
		if resp != nil {
			got = resp.StatusCode
		}
		t.Errorf("%s answered %d, want %d", requestOf(resp), got, want)
	}
}

func requestOf(resp *http.Response) string {
	if resp == nil || resp.Request == nil {
		return "the request"
	}

	return resp.Request.Method + " " + resp.Request.URL.Path
}

// poll calls f every half second until it returns true or the deadline
// passes, and reports whether it did.
func poll(timeout time.Duration, f func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if f() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// runCommand queues a command with its own fields and waits for it to
// finish, failing the test when it does not complete.
func runCommand(ctx context.Context, t *testing.T, name string, fields map[string]any) *sonarr.CommandResource {
	t.Helper()

	cmd, err := queueCommand(ctx, name, fields)
	if err != nil {
		t.Fatal(err)
	}
	if !poll(2*time.Minute, func() bool {
		cmd = must(sc.GetCommandById(ctx, cmd.Id)).Model
		return cmd.Status != sonarr.CommandStatusQueued && cmd.Status != sonarr.CommandStatusStarted
	}) {
		t.Fatalf("%s never finished: %+v", name, cmd)
	}
	if cmd.Status != sonarr.CommandStatusCompleted {
		t.Fatalf("%s %s: %s %s", name, cmd.Status, cmd.Message, cmd.Exception)
	}

	return cmd
}

// queueCommand posts a command: its name and its own fields, at the top level
// of the body, where CommandController reads them.
func queueCommand(ctx context.Context, name string, fields map[string]any) (*sonarr.CommandResource, error) {
	body := map[string]any{"name": name}
	maps.Copy(body, fields)
	raw, err := jsonRaw(body)
	if err != nil {
		return nil, err
	}
	res, err := sc.PostCommand(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	return res.Model, nil
}

// lookupSeries asks Sonarr, and through it SkyHook, for a show by its tvdb
// id.
func lookupSeries(ctx context.Context, tvdb int) (sonarr.SeriesResource, error) {
	res, err := sc.GetSeriesLookup(ctx, sonarr.GetSeriesLookupOperationOptions{Term: "tvdb:" + strconv.Itoa(tvdb)})
	if err != nil {
		return sonarr.SeriesResource{}, err
	}
	i := slices.IndexFunc(res.Model, func(s sonarr.SeriesResource) bool { return s.TvdbId == tvdb })
	if i < 0 {
		return sonarr.SeriesResource{}, fmt.Errorf("the lookup for tvdb:%d found %d shows, none of them it", tvdb, len(res.Model))
	}

	return res.Model[i], nil
}

// forAdding readies a looked-up show to be added: where it goes, its
// profile, and that nothing is searched for.
func forAdding(found *sonarr.SeriesResource, path string) sonarr.SeriesResource {
	show := *found
	show.QualityProfileId = profileID
	show.RootFolderPath = "/tv"
	if path != "" {
		show.Path = path
	}
	show.Monitored = new(true)
	show.SeasonFolder = new(true)
	show.MonitorNewItems = sonarr.NewItemMonitorTypesAll
	show.AddOptions = &sonarr.AddSeriesOptions{Monitor: sonarr.MonitorTypesAll, SearchForMissingEpisodes: new(false)}

	return show
}

// seed builds the library: the root folder, the fakes as Sonarr's indexer
// and download client, four series imported from their folders and one added
// with none. It is idempotent, so a suite can run again against a container
// that is still up.
func seed(ctx context.Context) error {
	profiles, err := sc.GetQualityProfile(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(profiles.Model, func(p sonarr.QualityProfileResource) bool { return p.Name == profileName })
	if i < 0 {
		return fmt.Errorf("no %s quality profile", profileName)
	}
	profileID = profiles.Model[i].Id

	roots, err := sc.GetRootFolder(ctx)
	if err != nil {
		return err
	}
	if j := slices.IndexFunc(roots.Model, func(r sonarr.RootFolderResource) bool { return r.Path == "/tv" }); j >= 0 {
		rootFolderID = roots.Model[j].Id
	} else {
		made, postErr := sc.PostRootFolder(ctx, sonarr.RootFolderResource{Path: "/tv"})
		if postErr != nil {
			return postErr
		}
		rootFolderID = made.Model.Id
	}

	if err := seedDownloads(ctx); err != nil {
		return err
	}

	existing, err := sc.GetSeries(ctx, sonarr.GetSeriesOperationOptions{})
	if err != nil {
		return err
	}
	have := map[int]int{}
	for i := range existing.Model {
		have[existing.Model[i].TvdbId] = existing.Model[i].Id
	}
	var imports []sonarr.SeriesResource
	for _, f := range seeded {
		if have[f.TvdbID] != 0 {
			continue
		}
		show, err := lookupSeries(ctx, f.TvdbID)
		if err != nil {
			return err
		}
		imports = append(imports, forAdding(&show, "/tv/"+f.Folder))
	}
	if len(imports) > 0 {
		if _, err := sc.PostSeriesImport(ctx, imports); err != nil {
			return fmt.Errorf("importing the series: %w", err)
		}
	}
	if have[breakingBad.TvdbID] == 0 {
		show, err := lookupSeries(ctx, breakingBad.TvdbID)
		if err != nil {
			return err
		}
		if _, err := sc.PostSeries(ctx, forAdding(&show, "")); err != nil {
			return fmt.Errorf("adding %s: %w", breakingBad.Title, err)
		}
	}

	// the import refreshes each series from SkyHook, then scans its folder; a
	// series already there has whatever files an earlier run left it
	for _, f := range append(slices.Clone(seeded), breakingBad) {
		var s sonarr.SeriesResource
		if !poll(2*time.Minute, func() bool {
			all, err := sc.GetSeries(ctx, sonarr.GetSeriesOperationOptions{TvdbId: f.TvdbID})
			if err != nil || len(all.Model) != 1 {
				return false
			}
			s = all.Model[0]
			return s.Statistics != nil && s.Statistics.TotalEpisodeCount > 0 && (have[f.TvdbID] != 0 || s.Statistics.EpisodeFileCount == f.Files)
		}) {
			return fmt.Errorf("%s never settled on %d files: %+v", f.Title, f.Files, s.Statistics)
		}
		seriesIDs[f.Title] = s.Id
	}

	return waitIdle(ctx)
}

// waitIdle waits for Sonarr to finish every command it has queued or
// running. A series just added is refreshed and scanned, and then saved
// whole once more as the add finishes; a test that edits it before that save
// has its edit quietly overwritten, so the tests start only once Sonarr is
// done.
func waitIdle(ctx context.Context) error {
	var busy []string
	if poll(time.Minute, func() bool {
		res, err := sc.GetCommand(ctx)
		if err != nil {
			return false
		}
		busy = busy[:0]
		for _, c := range res.Model {
			if c.Status == sonarr.CommandStatusQueued || c.Status == sonarr.CommandStatusStarted {
				busy = append(busy, c.Name)
			}
		}
		return len(busy) == 0
	}) {
		return nil
	}

	return fmt.Errorf("sonarr never finished %v", busy)
}

// seedDownloads points Sonarr at the fake indexer and download client, and
// turns off the automatic re-search after a failure, so a failure a test
// causes stays one failure rather than a fresh grab of the next release.
func seedDownloads(ctx context.Context) error {
	cfg, err := sc.GetConfigDownloadClient(ctx)
	if err != nil {
		return err
	}
	body := *cfg.Model
	body.AutoRedownloadFailed, body.AutoRedownloadFailedFromInteractiveSearch = new(false), new(false)
	if _, err := sc.PutConfigDownloadClientById(ctx, strconv.Itoa(body.Id), body); err != nil {
		return fmt.Errorf("download client settings: %w", err)
	}

	indexers, err := sc.GetIndexer(ctx)
	if err != nil {
		return err
	}
	var idx sonarr.IndexerResource
	if i := slices.IndexFunc(indexers.Model, func(x sonarr.IndexerResource) bool { return x.Name == sdkIndexer }); i >= 0 {
		idx = indexers.Model[i]
	} else {
		made, postErr := sc.PostIndexer(ctx, newIndexer(ctx, sdkIndexer), sonarr.PostIndexerOperationOptions{})
		if postErr != nil {
			return fmt.Errorf("adding the fake indexer: %w", postErr)
		}
		idx = *made.Model
	}
	indexerID = idx.Id

	clients, err := sc.GetDownloadClient(ctx)
	if err != nil {
		return err
	}
	var dc sonarr.DownloadClientResource
	if i := slices.IndexFunc(clients.Model, func(x sonarr.DownloadClientResource) bool { return x.Name == sdkClient }); i >= 0 {
		dc = clients.Model[i]
	} else {
		made, err := sc.PostDownloadClient(ctx, newDownloadClient(ctx, sdkClient), sonarr.PostDownloadClientOperationOptions{})
		if err != nil {
			return fmt.Errorf("adding the fake download client: %w", err)
		}
		dc = *made.Model
	}
	downloadClient = dc.Id

	// a container left up between runs has tried the fakes while they were
	// down, and holds back a provider that failed until a test of it passes
	// (IndexerFactory.Test and DownloadClientFactory.Test record either)
	if _, err := sc.PostIndexerTest(ctx, idx, sonarr.PostIndexerTestOperationOptions{}); err != nil {
		return fmt.Errorf("testing the fake indexer: %w", err)
	}
	if _, err := sc.PostDownloadClientTest(ctx, dc, sonarr.PostDownloadClientTestOperationOptions{}); err != nil {
		return fmt.Errorf("testing the fake download client: %w", err)
	}

	return nil
}

// The names of what the seed adds.
const (
	sdkIndexer = "SDK Indexer"
	sdkClient  = "SDK SABnzbd"
)

// newIndexer is a Newznab indexer at the fake, from Sonarr's own schema.
func newIndexer(ctx context.Context, name string) sonarr.IndexerResource {
	schemas := must(sc.GetIndexerSchema(ctx)).Model
	i := slices.IndexFunc(schemas, func(s sonarr.IndexerResource) bool { return s.Implementation == "Newznab" })
	if i < 0 {
		panic("sonarr has no Newznab indexer schema")
	}
	idx := schemas[i]
	idx.Name, idx.EnableRss, idx.EnableAutomaticSearch, idx.EnableInteractiveSearch = name, new(true), new(true), new(true)
	idx.Fields = fields(idx.Fields, map[string]any{
		"baseUrl": indexer.URL(), "apiPath": "/api", "apiKey": fakeKey, "categories": []int{5030, 5040, 5045},
	})

	return idx
}

// newDownloadClient is a SABnzbd client at the fake, from Sonarr's own
// schema.
func newDownloadClient(ctx context.Context, name string) sonarr.DownloadClientResource {
	schemas := must(sc.GetDownloadClientSchema(ctx)).Model
	i := slices.IndexFunc(schemas, func(s sonarr.DownloadClientResource) bool { return s.Implementation == "Sabnzbd" })
	if i < 0 {
		panic("sonarr has no Sabnzbd download client schema")
	}
	dc := schemas[i]
	dc.Name, dc.Enable = name, new(true)
	dc.Fields = fields(dc.Fields, map[string]any{
		"host": sab.Host(), "port": sab.Port(), "apiKey": fakeKey, "tvCategory": sab.Category(), "useSsl": false, "urlBase": "",
	})

	return dc
}

// fields fills a provider schema's fields from values by name.
func fields(schema []sonarr.Field, values map[string]any) []sonarr.Field {
	out := slices.Clone(schema)
	for i := range out {
		if v, ok := values[out[i].Name]; ok {
			out[i].Value = v
		}
	}

	return out
}

// fieldValue reads one field of a provider back.
func fieldValue(fs []sonarr.Field, name string) any {
	for _, f := range fs {
		if f.Name == name {
			return f.Value
		}
	}

	return nil
}

// seriesByTitle is one of the seeded series, read fresh.
func seriesByTitle(ctx context.Context, t *testing.T, title string) sonarr.SeriesResource {
	t.Helper()

	id, ok := seriesIDs[title]
	if !ok {
		t.Fatalf("%s was not seeded", title)
	}

	return *must(sc.GetSeriesById(ctx, id, sonarr.GetSeriesByIdOperationOptions{})).Model
}

// episodesOf reads a series' episodes, with their files.
func episodesOf(ctx context.Context, seriesID int) []sonarr.EpisodeResource {
	return must(sc.GetEpisode(ctx, sonarr.GetEpisodeOperationOptions{SeriesId: seriesID, IncludeEpisodeFile: new(true)})).Model
}

// episode finds an episode of a series' first season, where every fixture's
// files and releases are.
func episode(ctx context.Context, t *testing.T, seriesID, number int) sonarr.EpisodeResource {
	t.Helper()

	for _, e := range episodesOf(ctx, seriesID) {
		if e.SeasonNumber == 1 && e.EpisodeNumber == number {
			return e
		}
	}
	t.Fatalf("series %d has no S01E%02d", seriesID, number)

	return sonarr.EpisodeResource{}
}

// isStatus reports whether err is a Sonarr refusal with the given status.
func isStatus(err error, code int) bool { return client.StatusCode(err) == code }

// unmarshal decodes a raw JSON answer.
func unmarshal(raw json.RawMessage, v any) error { return json.Unmarshal(raw, v) }

// mkdirAll makes a directory under the host side of the container's mounts
// that Sonarr's own user can write in. MkdirAll's mode is filtered by the
// umask, which on Linux leaves a directory only the test can write; chmod is
// not.
func mkdirAll(t *testing.T, base string, parts ...string) string {
	t.Helper()

	dir := filepath.Join(append([]string{base}, parts...)...)
	if err := os.MkdirAll(dir, 0o777); err != nil { //nolint:gosec // the container writes it as another user
		t.Fatal(err)
	}
	for p := dir; strings.HasPrefix(p, base) && p != base; p = filepath.Dir(p) {
		if err := os.Chmod(p, 0o777); err != nil { //nolint:gosec // same
			t.Fatal(err)
		}
	}

	return dir
}
