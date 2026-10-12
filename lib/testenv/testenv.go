// Package testenv is what the two live suites share that is this
// application's own: the environment .scripts/testenv.sh hands them, how the
// record/replay proxy Sonarr's provider calls go through is set up, the fake
// indexer and download client Sonarr searches and downloads with, and the
// container, which is started here once those are listening. The machinery is
// go-kt's (test/env, test/replayproxy and the stand-ins under test/services).
//
// The tool suite (../../acceptance/tools) drives the tools over MCP and the
// SDK suite (../../acceptance/sonarr) drives the generated SDK; each runs
// against a container, a library and ports of its own.
package testenv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/katbyte/go-kt/test/env"
	"github.com/katbyte/go-kt/test/replayproxy"
	"github.com/katbyte/go-kt/test/services/arrserver"
	"github.com/katbyte/go-kt/test/services/newznab"
	"github.com/katbyte/go-kt/test/services/sabnzbd"
	"github.com/katbyte/sonarr-mcp/internal/cassettes"
)

// sonarr is the environment .scripts/testenv.sh writes, every variable of
// which begins SONARR_.
var sonarr = env.New("SONARR")

// NotConfigured is what a test says when it skips for want of a container.
const NotConfigured = "SONARR_SERVER, SONARR_TOKEN and SONARR_TEST_CONTAINER are not set; run: eval \"$(.scripts/testenv.sh up)\""

// FakeKey is the API key both fakes demand, so a Sonarr that sent none would
// be caught.
const FakeKey = "sonarr-mcp-fake"

// Configured reports whether the container environment is present: a server,
// a token and the container to start.
func Configured() bool { return sonarr.Configured("TEST_CONTAINER") }

// Server is the address of the Sonarr under test (SONARR_SERVER).
func Server() string { return sonarr.Server() }

// Token is its API key (SONARR_TOKEN).
func Token() string { return sonarr.Token() }

// Recording reports whether this run may call the real providers
// (SONARR_TEST_RECORD): it records the answers a cassette lacks and replays
// the rest.
func Recording() bool { return sonarr.Recording() }

// DataDir is the host directory the container's /tv, /downloads and /config
// are bind-mounted from, with elem joined onto it, so a test can add or
// remove files on disk. It is "" when SONARR_TEST_DATA is not set.
func DataDir(elem ...string) string { return sonarr.DataDir(elem...) }

// Host is how the container reaches this process: the name or address
// .scripts/testenv.sh pointed its proxy settings at (SONARR_TEST_HOST).
func Host() string { return sonarr.Host() }

// Ports are the defaults a suite's fake indexer and download client listen
// on when the environment names none. Each suite has its own, so the two can
// run side by side.
type Ports struct {
	Indexer, SAB int
}

// Proxy is the record/replay proxy a suite runs for the length of its run,
// and what it saw once stopped.
type Proxy = env.Proxy

// ListenProxy brings up the record/replay proxy the container's HTTPS_PROXY
// already points at, signing with the CA .scripts/testenv.sh minted and
// mounted into the container, with its cassettes under the suite's testdata.
// Of the providers' lists of every show they know, a recording keeps only the
// series with these TheTVDB ids (internal/cassettes).
//
// It asks the container nothing, because the container is not running yet:
// Sonarr calls services.sonarr.tv as it starts, and a call made before the
// proxy is up is neither recorded nor replayed. StartContainer starts it.
func ListenProxy(ctx context.Context, tvdbIDs ...int) (*Proxy, error) {
	p, err := sonarr.ListenProxy(ctx, replayproxy.Options{
		CassetteDir: filepath.Join("testdata", "cassettes"),
		// services.sonarr.tv is asked with the machine's architecture, which is
		// Arm64 on a Mac and X64 on a runner: a cassette keyed on it would only
		// replay where it was recorded
		RedactQuery: []string{"arch"},
		// a request for this process's own address is not provider traffic,
		// and nor is the server reaching itself, whose addresses go-kt adds
		IgnoreHosts: []string{Host()},
		Trim:        cassettes.ForSeries(tvdbIDs...),
	})
	if err != nil {
		return nil, err
	}

	// Sonarr calls home whether or not anyone asked it to, and two of its
	// questions cannot be answered from a recording. The time: it holds its
	// own clock to the answer and reports a health error when they are a day
	// apart, and a recording answers the day it was made. Whether there is an
	// update: it asks only once its build is two weeks old, so a recording
	// made in a release's first fortnight has no answer, and an unanswered
	// one ends the whole run of health checks. go-kt's stand-in answers both,
	// the time as it is and the releases there are (the one running, and
	// what a test offers); the rest of the host - the scene mappings, the
	// daily series - is replayed.
	home := arrserver.New()
	for _, path := range homePaths {
		p.Serve(homeHost+path, home)
	}
	Updates = home
	OfferUpdates()

	return p, nil
}

// Updates is the stand-in Sonarr asks what releases there are and whether
// one is newer than the running one; it is set when the proxy is started.
// It knows of the release running (SonarrVersion), so the list Sonarr
// answers is never empty, and of whatever a test offers beside it
// (OfferUpdates).
var Updates *arrserver.Server

// OfferUpdates has the stand-in know of the release running and of extra,
// in place of whatever it knew: a test offers a newer release with it, and
// takes it back with none.
func OfferUpdates(extra ...arrserver.Update) {
	Updates.Offer(append([]arrserver.Update{{Version: SonarrVersion(), Fixed: []string{"the release running in the test container"}}}, extra...)...)
}

// defaultVersion is the Sonarr release .scripts/testenv.sh runs unless
// SONARR_TEST_IMAGE names another.
const defaultVersion = "4.0.20.3014"

// SonarrVersion is the release the test container runs, from the image's
// tag (lscr.io/linuxserver/sonarr:version-4.0.20.3014).
func SonarrVersion() string {
	if _, v, ok := strings.Cut(os.Getenv("SONARR_TEST_IMAGE"), ":version-"); ok && v != "" {
		return v
	}

	return defaultVersion
}

// homeHost is where Sonarr calls home to, and homePaths are what the suite
// answers there itself rather than from a recording.
const homeHost = "services.sonarr.tv"

var homePaths = []string{"/v1/time", "/v1/update/main", "/v1/update/main/changes"}

// IndexerName is the name the fake indexer is served under, which its
// address ends in: one listener serves any number of them.
const IndexerName = "indexer"

// Fakes are the indexer and the download client a suite runs for Sonarr to
// search and download with.
type Fakes struct {
	Indexer *newznab.Indexer
	SAB     *sabnzbd.Server

	indexers *newznab.Server
	ports    []port
}

// port is something the container has to reach on this process, by name.
type port struct {
	what   string
	number int
}

// StartFakes starts the fake indexer, offering releases, and the fake
// download client, on the ports .scripts/testenv.sh told the container about
// (SONARR_TEST_INDEXER_PORT and SONARR_TEST_SAB_PORT, or the suite's
// defaults), and waits for both to answer.
func StartFakes(defaults Ports, releases []newznab.Release) (*Fakes, error) {
	iport, err := portFrom("TEST_INDEXER_PORT", defaults.Indexer)
	if err != nil {
		return nil, err
	}
	sport, err := portFrom("TEST_SAB_PORT", defaults.SAB)
	if err != nil {
		return nil, err
	}

	f := &Fakes{ports: []port{{"the fake indexer", iport}, {"the fake download client", sport}}}
	seed, err := seedFromEnv()
	if err != nil {
		return nil, err
	}
	if f.indexers, err = newznab.New(newznab.Options{Addr: ":" + strconv.Itoa(iport), PublicHost: Host(), Seed: seed, Sites: []newznab.Site{{Name: IndexerName, APIKey: FakeKey, Releases: releases}}}); err != nil {
		return nil, err
	}
	f.Indexer = f.indexers.Indexer(IndexerName)
	// what a release does not say and a feed has to carry is picked afresh
	// each run, so no test leans on a made-up value without knowing it. The
	// indexer says the seed it picked; this says how to hand it back
	_, _ = fmt.Fprintf(os.Stderr, "%s=%d runs the fake indexer on the same data again\n", sonarr.Var(seedVar), f.indexers.Seed())
	if f.SAB, err = sabnzbd.New(sabnzbd.Options{Addr: ":" + strconv.Itoa(sport), APIKey: FakeKey, PublicHost: Host(), CompleteDir: "/downloads/complete", HostCompleteDir: DataDir("downloads", "complete")}); err != nil {
		f.Stop()

		return nil, err
	}

	if err := WaitHTTP(f.Indexer.LocalURL() + "/api?t=caps"); err != nil {
		f.Stop()

		return nil, fmt.Errorf("the fake indexer never answered: %w", err)
	}
	if err := WaitHTTP(f.SAB.LocalURL() + "/api?mode=version"); err != nil {
		f.Stop()

		return nil, fmt.Errorf("the fake download client never answered: %w", err)
	}

	return f, nil
}

// AddIndexer serves another indexer beside the first, under its own name and
// with the same key, for a test that wants one to break or to compare with.
// It lasts as long as the fakes do, and asking again for one that is there
// answers that one.
func (f *Fakes) AddIndexer(name string, releases ...newznab.Release) (*newznab.Indexer, error) {
	if ix := f.indexers.Indexer(name); ix != nil {
		return ix, nil
	}

	return f.indexers.Add(newznab.Site{Name: name, APIKey: FakeKey, Releases: releases})
}

// AddTorrentIndexer serves another indexer beside the first, a torrent one:
// its feed is Torznab's, and what it hands a grab is a .torrent.
func (f *Fakes) AddTorrentIndexer(name string, releases ...newznab.Release) (*newznab.Indexer, error) {
	if ix := f.indexers.Indexer(name); ix != nil {
		return ix, nil
	}

	return f.indexers.Add(newznab.Site{Name: name, Protocol: newznab.Torrent, APIKey: FakeKey, Releases: releases})
}

// Stop closes both fakes. Stopping what never started is harmless.
func (f *Fakes) Stop() {
	if f == nil {
		return
	}
	if f.indexers != nil {
		_ = f.indexers.Close()
	}
	if f.SAB != nil {
		_ = f.SAB.Close()
	}
}

// seedVar names the fake indexer's seed, SONARR_TEST_SEED.
const seedVar = "TEST_SEED"

// seedFromEnv reads the fake indexer's seed from the environment; unset is
// 0, which has the indexer pick one.
func seedFromEnv() (uint64, error) {
	v := sonarr.Get(seedVar)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", sonarr.Var(seedVar), v, err)
	}

	return n, nil
}

// portFrom reads a port from the environment, with its default.
func portFrom(name string, def int) (int, error) {
	v := sonarr.Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", sonarr.Var(name), v, err)
	}

	return n, nil
}

// WaitHTTP polls a URL until it answers, for a server that has just started:
// a fake, or the binary under test.
func WaitHTTP(url string) error {
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

// StartContainer starts the container .scripts/testenv.sh created, now that
// the proxy and the fakes are listening, and proves from inside it that
// Sonarr can reach each of them. One it cannot reach fails every lookup,
// search and download with a timeout of its own, which reads as dozens of
// unrelated failures rather than the one plumbing problem it is - so it is
// said plainly, once, before the suite runs.
func StartContainer(ctx context.Context, fakes *Fakes) error {
	name := sonarr.Container()
	if out, err := exec.CommandContext(ctx, "docker", "start", name).CombinedOutput(); err != nil { //nolint:gosec // the container the test script created, named by the environment it wrote
		return fmt.Errorf("docker start %s: %w: %s", name, err, out)
	}

	proxyPort, err := sonarr.ProxyPort()
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range append([]port{{"the replay proxy", proxyPort}}, fakes.ports...) {
		if _, err := sonarr.CheckReachable(ctx, p.what, p.number); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
