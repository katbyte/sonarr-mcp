//go:build acceptance

// The harness: .scripts/testenv.sh creates the container and exports
// SONARR_SERVER, SONARR_TOKEN and SONARR_TEST_*; runSuite starts the provider
// proxy and the fakes, then the container (lib/testenv), builds the SDK client
// so it records every request, seeds the library, runs the suite, and checks
// every operation was called.
//
// What is deliberately not exercised, and why, is in notCalled (coverage.go
// of this package): the operations that restart or stop Sonarr, restore a
// backup over it, or answer only a browser.
package sonarr_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/go-kt/test/services/sabnzbd"
	"github.com/katbyte/pandorest/client"
	"github.com/katbyte/sonarr-mcp/lib/testenv"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
)

// seriesFixture is a series in the library and what Sonarr should hold for
// it once seeded.
type seriesFixture struct {
	Title  string
	TvdbID sonarr.TvdbID
	// Folder is its folder under /tv; "" for a series added with no files.
	Folder string
	// Files is how many episode files Sonarr should import from the folder.
	Files int
}

// The series, as .scripts/testenv.sh lays them out. The suite only reads
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
		ids = append(ids, int(s.TvdbID))
	}

	return ids
}

// profileName is the quality profile the seeded series are on.
const profileName = "HD-1080p"

var (
	sc *sonarr.Client

	proxy *testenv.Proxy
	fakes *testenv.Fakes
	// the fake indexer and download client, as the tests name them
	indexer *newznab.Indexer
	sab     *sabnzbd.Server

	// what the seed made, for the tests to find
	seriesIDs      = map[string]sonarr.SeriesID{}
	rootFolderID   int
	profileID      sonarr.QualityProfileID
	indexerID      int
	downloadClient int
)

// fakePorts are the ports the fakes listen on when the environment names
// none: the suite's own, so it can run beside the tool suite.
var fakePorts = testenv.Ports{Indexer: 18181, SAB: 18182}

// runSuite starts everything, seeds the library, runs the tests, and returns
// the exit code.
func runSuite(m *testing.M) int {
	if !testenv.Configured() {
		return m.Run() // every test skips
	}
	ctx := context.Background()
	defer stopAll()

	var err error
	if proxy, err = testenv.ListenProxy(ctx, tvdbIDs(everySeries)...); err != nil {
		fmt.Fprintln(os.Stderr, "provider proxy:", err)
		return 1
	}
	if fakes, err = testenv.StartFakes(fakePorts, catalogue()); err != nil {
		fmt.Fprintln(os.Stderr, "fakes:", err)
		return 1
	}
	indexer, sab = fakes.Indexer, fakes.SAB
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
	// and a drift that a provider no longer answers in the shape Sonarr
	// decodes; either fails the run even when the assertions survived it
	if report := proxy.Report(); report != "" {
		fmt.Fprint(os.Stderr, report)
		if code == 0 {
			code = 1
		}
	}
	// every operation must have been called by something above, or be
	// named in notCalled. Only a whole-suite run can say that.
	if f := flag.Lookup("test.run"); f == nil || f.Value.String() == "" {
		problems, total, err := uncovered()
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "\noperation coverage:", err)
			code = 1
		case len(problems) > 0:
			fmt.Fprintf(os.Stderr, "\noperation coverage: %d to put right, in the tests or in notCalled:\n", len(problems))
			for _, problem := range problems {
				fmt.Fprintln(os.Stderr, "  "+problem)
			}
			code = 1
		default:
			fmt.Fprintf(os.Stderr, "\noperation coverage: %d of %d operations called; the other %d are in notCalled\n", total-len(notCalled), total, len(notCalled))
		}
	}

	return code
}

// stopAll stops the fakes and the proxy, which keeps what it saw.
func stopAll() {
	fakes.Stop()
	if err := proxy.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

// startSonarr starts the container once the proxy and fakes are listening,
// checks it can reach them, builds the client, and waits for Sonarr.
func startSonarr(ctx context.Context) error {
	if err := testenv.StartContainer(ctx, fakes); err != nil {
		return err
	}

	c, err := sonarr.New(testenv.Server(), testenv.Token(), client.WithCalls(&calls))
	if err != nil {
		return err
	}
	sc = c
	// a command's wait looks every two seconds unless told otherwise, and the suite waits for many that end at once
	sonarr.CommandPoll = 500 * time.Millisecond

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

// commandWait is how long a test waits for a command it queued.
const commandWait = 2 * time.Minute

// runCommand queues by name a command the client has no method of its own for, and waits for it to finish, failing the test when it does not
// complete. A command with a method is queued and waited for by its AndWait form.
func runCommand(ctx context.Context, t *testing.T, name string) *sonarr.CommandResource {
	t.Helper()

	cmd, err := queueCommand(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	done, err := sc.WaitForCommand(ctx, cmd.Id, commandWait)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	return done.Answer
}

// queueCommand posts a command by its name alone, the plain way.
func queueCommand(ctx context.Context, name string) (*sonarr.CommandResource, error) {
	raw, err := jsonRaw(map[string]string{"name": name})
	if err != nil {
		return nil, err
	}
	res, err := sc.PostCommand(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	return res.Answer, nil
}

// lookupSeries asks Sonarr, and through it SkyHook, for a show by its tvdb
// id.
func lookupSeries(ctx context.Context, tvdb sonarr.TvdbID) (sonarr.SeriesResource, error) {
	res, err := sc.GetSeriesLookup(ctx, sonarr.GetSeriesLookupOptions{Term: "tvdb:" + strconv.Itoa(int(tvdb))})
	if err != nil {
		return sonarr.SeriesResource{}, err
	}
	i := slices.IndexFunc(res.Answer, func(s sonarr.SeriesResource) bool { return s.TvdbId == tvdb })
	if i < 0 {
		return sonarr.SeriesResource{}, fmt.Errorf("the lookup for tvdb:%d found %d shows, none of them it", tvdb, len(res.Answer))
	}

	return res.Answer[i], nil
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
	i := slices.IndexFunc(profiles.Answer, func(p sonarr.QualityProfileResource) bool { return p.Name == profileName })
	if i < 0 {
		return fmt.Errorf("no %s quality profile", profileName)
	}
	profileID = profiles.Answer[i].Id

	roots, err := sc.GetRootFolder(ctx)
	if err != nil {
		return err
	}
	if j := slices.IndexFunc(roots.Answer, func(r sonarr.RootFolderResource) bool { return r.Path == "/tv" }); j >= 0 {
		rootFolderID = roots.Answer[j].Id
	} else {
		made, postErr := sc.PostRootFolder(ctx, sonarr.RootFolderResource{Path: "/tv"})
		if postErr != nil {
			return postErr
		}
		rootFolderID = made.Answer.Id
	}

	if err := seedDownloads(ctx); err != nil {
		return err
	}

	existing, err := sc.GetSeries(ctx, sonarr.GetSeriesOptions{})
	if err != nil {
		return err
	}
	have := map[sonarr.TvdbID]sonarr.SeriesID{}
	for i := range existing.Answer {
		have[existing.Answer[i].TvdbId] = existing.Answer[i].Id
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
			all, err := sc.GetSeries(ctx, sonarr.GetSeriesOptions{TvdbId: f.TvdbID})
			if err != nil || len(all.Answer) != 1 {
				return false
			}
			s = all.Answer[0]
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
		for _, c := range res.Answer {
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
	body := must(cfg.Record())
	body.AutoRedownloadFailed, body.AutoRedownloadFailedFromInteractiveSearch = false, false
	if _, err := sc.PutConfigDownloadClientById(ctx, body.Id, body); err != nil {
		return fmt.Errorf("download client settings: %w", err)
	}

	indexers, err := sc.GetIndexer(ctx)
	if err != nil {
		return err
	}
	var idx sonarr.IndexerResource
	if i := slices.IndexFunc(indexers.Answer, func(x sonarr.IndexerResource) bool { return x.Name == sdkIndexer }); i >= 0 {
		idx = indexers.Answer[i]
	} else {
		made, postErr := sc.PostIndexer(ctx, newIndexer(ctx, sdkIndexer), sonarr.PostIndexerOptions{})
		if postErr != nil {
			return fmt.Errorf("adding the fake indexer: %w", postErr)
		}
		idx = must(made.Record())
	}
	indexerID = idx.Id

	clients, err := sc.GetDownloadClient(ctx)
	if err != nil {
		return err
	}
	var dc sonarr.DownloadClientResource
	if i := slices.IndexFunc(clients.Answer, func(x sonarr.DownloadClientResource) bool { return x.Name == sdkClient }); i >= 0 {
		dc = clients.Answer[i]
	} else {
		made, err := sc.PostDownloadClient(ctx, newDownloadClient(ctx, sdkClient), sonarr.PostDownloadClientOptions{})
		if err != nil {
			return fmt.Errorf("adding the fake download client: %w", err)
		}
		dc = must(made.Record())
	}
	downloadClient = dc.Id

	// a container left up between runs has tried the fakes while they were
	// down, and holds back a provider that failed until a test of it passes
	// (IndexerFactory.Test and DownloadClientFactory.Test record either)
	if _, err := sc.PostIndexerTest(ctx, idx, sonarr.PostIndexerTestOptions{}); err != nil {
		return fmt.Errorf("testing the fake indexer: %w", err)
	}
	if _, err := sc.PostDownloadClientTest(ctx, dc, sonarr.PostDownloadClientTestOptions{}); err != nil {
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
	schemas := must(sc.GetIndexerSchema(ctx)).Answer
	i := slices.IndexFunc(schemas, func(s sonarr.IndexerResource) bool { return s.Implementation == "Newznab" })
	if i < 0 {
		panic("sonarr has no Newznab indexer schema")
	}
	idx := schemas[i]
	idx.Name, idx.EnableRss, idx.EnableAutomaticSearch, idx.EnableInteractiveSearch = name, new(true), new(true), new(true)
	idx.Fields = fields(idx.Fields, map[string]any{
		"baseUrl": indexer.URL(), "apiPath": "/api", "apiKey": testenv.FakeKey, "categories": []int{5030, 5040, 5045},
	})

	return idx
}

// newDownloadClient is a SABnzbd client at the fake, from Sonarr's own
// schema.
func newDownloadClient(ctx context.Context, name string) sonarr.DownloadClientResource {
	schemas := must(sc.GetDownloadClientSchema(ctx)).Answer
	i := slices.IndexFunc(schemas, func(s sonarr.DownloadClientResource) bool { return s.Implementation == "Sabnzbd" })
	if i < 0 {
		panic("sonarr has no Sabnzbd download client schema")
	}
	dc := schemas[i]
	dc.Name, dc.Enable = name, new(true)
	dc.Fields = fields(dc.Fields, map[string]any{
		"host": sab.Host(), "port": sab.Port(), "apiKey": testenv.FakeKey, "tvCategory": sab.Category(), "useSsl": false, "urlBase": "",
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

	return *must(sc.GetSeriesById(ctx, id, sonarr.GetSeriesByIdOptions{})).Answer
}

// episodesOf reads a series' episodes, with their files.
func episodesOf(ctx context.Context, seriesID sonarr.SeriesID) []sonarr.EpisodeResource {
	return must(sc.GetEpisode(ctx, sonarr.GetEpisodeOptions{SeriesId: seriesID, IncludeEpisodeFile: new(true)})).Answer
}

// episode finds an episode of a series' first season, where every fixture's
// files and releases are.
func episode(ctx context.Context, t *testing.T, seriesID sonarr.SeriesID, number int) sonarr.EpisodeResource {
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
