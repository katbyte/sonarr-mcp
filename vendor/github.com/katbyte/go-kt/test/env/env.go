// Package env is what a suite run against a live server in a container is
// handed: the environment its test script exports, the record/replay proxy
// the server's calls to the internet go through, the checks that the
// container can reach it, the report at the end of a run on what the proxy
// saw, and files laid out where the container reads them.
//
// Every variable carries the app's prefix: New("APP") reads APP_SERVER,
// APP_TOKEN, APP_TEST_RECORD and the rest. A suite that drives the tools over
// MCP and one that drives a generated SDK share the same containers and
// recordings, so both read them through here.
package env

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/katbyte/go-kt/test/replayproxy"
)

// DefaultProxyPort is the port the proxy listens on when the environment
// names none.
const DefaultProxyPort = 18080

// Env reads one app's test environment.
type Env struct {
	prefix string
	getenv func(string) string
}

// New is the environment under prefix, without its underscore: "APP" for
// APP_SERVER.
func New(prefix string) Env {
	return Env{prefix: strings.TrimSuffix(prefix, "_"), getenv: os.Getenv}
}

// Var is the full name of one of the app's variables: Var("TEST_DATA") is
// APP_TEST_DATA.
func (e Env) Var(name string) string { return e.prefix + "_" + name }

// Get is the value of one of the app's variables, "" when it is not set.
func (e Env) Get(name string) string { return e.getenv(e.Var(name)) }

// Server is the address of the server under test (APP_SERVER).
func (e Env) Server() string { return e.Get("SERVER") }

// Token is the credential for it (APP_TOKEN).
func (e Env) Token() string { return e.Get("TOKEN") }

// Configured reports whether the container environment is present: a server
// and a token, and each of the other variables a suite cannot run without.
func (e Env) Configured(also ...string) bool {
	for _, name := range append([]string{"SERVER", "TOKEN"}, also...) {
		if e.Get(name) == "" {
			return false
		}
	}

	return true
}

// Recording reports whether this run may call the real services.
// APP_TEST_RECORD=1 fills in only the answers a cassette lacks, replaying the
// rest as recorded; APP_TEST_RECORD=all fetches every answer afresh.
func (e Env) Recording() bool { return e.Get("TEST_RECORD") != "" }

// Verifying reports whether to check the cassettes against the live services
// without rewriting them (APP_TEST_VERIFY).
func (e Env) Verifying() bool { return e.Get("TEST_VERIFY") != "" }

// Mode is how the environment asks the proxy to run: replaying unless told
// to record or to verify, and recording when told both.
func (e Env) Mode() replayproxy.Mode {
	switch {
	case strings.EqualFold(e.Get("TEST_RECORD"), "all"):
		return replayproxy.Rerecord
	case e.Recording():
		return replayproxy.Record
	case e.Verifying():
		return replayproxy.Verify
	default:
		return replayproxy.Replay
	}
}

// DataDir is the host path the container's data is bind-mounted from
// (APP_TEST_DATA), with elem joined onto it, so a test can add or remove
// files and have the server see them. It is "" when the variable is not set,
// which every test that lays files out must skip on: joined onto nothing, the
// path would be a relative one, and the test would write into the checkout.
func (e Env) DataDir(elem ...string) string {
	root := e.Get("TEST_DATA")
	if root == "" {
		return ""
	}

	return filepath.Join(append([]string{root}, elem...)...)
}

// ProxyPort is the port the proxy listens on, APP_TEST_PROXY_PORT or
// DefaultProxyPort, which the container's HTTPS_PROXY already names.
func (e Env) ProxyPort() (int, error) {
	v := e.Get("TEST_PROXY_PORT")
	if v == "" {
		return DefaultProxyPort, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", e.Var("TEST_PROXY_PORT"), v, err)
	}

	return n, nil
}

// Container is the name of the container the test script started
// (APP_TEST_CONTAINER), "" when the server under test is not one of ours.
func (e Env) Container() string { return e.Get("TEST_CONTAINER") }

// DefaultHost is the name a container reaches the machine it runs on by when
// the environment names none.
const DefaultHost = "host.docker.internal"

// Host is the name the container reaches this machine by (APP_TEST_HOST),
// DefaultHost when the test script exports none. On Linux a script may hand
// the container the bridge gateway's address instead, so that a runtime
// preferring an IPv6 answer cannot pick a route the host does not listen on.
func (e Env) Host() string {
	if h := e.Get("TEST_HOST"); h != "" {
		return h
	}

	return DefaultHost
}

// Proxy is the record/replay proxy a suite runs for the length of its run,
// and what it saw once stopped.
type Proxy struct {
	proxy     *replayproxy.Proxy
	recordVar string
	// Misses are the requests replay had no recording for, and Drifts the
	// answers that changed shape since recording (when verifying); both are
	// read when the proxy is stopped
	Misses []string
	Drifts []replayproxy.Drift
}

// StartProxy brings up the record/replay proxy the container's HTTPS_PROXY
// already points at (ListenProxy), and proves the container can reach it,
// saying on stderr what the container sees of the network. The container has
// to be running: a suite that starts it only once the proxy is up calls
// ListenProxy, starts it, and then CheckReachable.
func (e Env) StartProxy(ctx context.Context, opts replayproxy.Options) (*Proxy, error) {
	p, err := e.ListenProxy(ctx, opts)
	if err != nil {
		return nil, err
	}

	network, err := e.CheckReachable(ctx, "the replay proxy", p.proxy.Port())
	if network != "" {
		_, _ = fmt.Fprintf(os.Stderr, "container network: %s\n", network)
	}
	if err != nil {
		_ = p.Stop() //nolint:contextcheck // closing takes no context

		return nil, err
	}

	return p, nil
}

// ListenProxy brings the proxy up and asks the container nothing, for a
// server that calls out as it starts: a call made before the proxy listens is
// neither recorded nor replayed, so the suite starts the container only once
// this returns.
//
// The environment decides how it runs and where it listens: opts.Mode is
// Env.Mode whatever it was set to; an empty opts.Addr is every interface on
// ProxyPort; and with no authority named, the files ca.pem and ca.key under
// APP_TEST_PROXY_CA are used when that is set, the directory the test script
// minted one in and mounted into the container. The addresses the server
// reaches itself on (ContainerAddresses) are added to opts.IgnoreHosts. The
// rest - the cassettes, what to redact, what else to ignore - is the suite's.
func (e Env) ListenProxy(ctx context.Context, opts replayproxy.Options) (*Proxy, error) {
	opts, err := e.proxyOptions(ctx, opts)
	if err != nil {
		return nil, err
	}

	p, err := replayproxy.New(opts) //nolint:contextcheck // the proxy outlives this call: it runs for the whole suite
	if err != nil {
		return nil, err
	}

	return &Proxy{proxy: p, recordVar: e.Var("TEST_RECORD")}, nil
}

// proxyOptions is a suite's options for its proxy with what the environment
// decides filled in (StartProxy).
func (e Env) proxyOptions(ctx context.Context, opts replayproxy.Options) (replayproxy.Options, error) {
	port, err := e.ProxyPort()
	if err != nil {
		return opts, err
	}

	opts.Mode = e.Mode()
	if opts.Addr == "" {
		// every interface and both stacks: the container reaches this
		// through Host, by default a name docker maps to the host gateway,
		// and a runner that hands the container an IPv6 route as well
		// would find nothing listening on an IPv4-only socket
		opts.Addr = ":" + strconv.Itoa(port)
	}
	if ca := e.Get("TEST_PROXY_CA"); ca != "" && opts.CACert == "" && opts.CAKey == "" {
		opts.CACert, opts.CAKey = filepath.Join(ca, "ca.pem"), filepath.Join(ca, "ca.key")
	}
	if opts.RecordHint == "" {
		opts.RecordHint = "record it with " + e.Var("TEST_RECORD") + "=1, which records only what is missing"
	}
	// the server reaching itself is nothing a recording is about
	opts.IgnoreHosts = append(append([]string(nil), opts.IgnoreHosts...), e.ContainerAddresses(ctx)...)

	return opts, nil
}

// Addr is the address the proxy listens on.
func (p *Proxy) Addr() string { return p.proxy.Addr() }

// Transport is for a client in the test's own process that should go through
// the proxy too, such as a tool that asks a service itself: it trusts the
// proxy's certificates (replayproxy.Proxy.Transport).
func (p *Proxy) Transport() *http.Transport { return p.proxy.Transport() }

// Serve answers every request for a host, or for one path of it when target
// names a path too, with h rather than a recording, until the returned func
// is called (replayproxy.Proxy.Serve).
func (p *Proxy) Serve(target string, h http.Handler) (stop func()) { return p.proxy.Serve(target, h) }

// Stop closes the proxy and keeps what it saw, returning what closing it
// failed with. Stopping twice, or stopping what never started, is harmless.
func (p *Proxy) Stop() error {
	if p == nil || p.proxy == nil {
		return nil
	}
	p.Misses = p.proxy.Misses()
	p.Drifts = p.proxy.Drifts()
	err := p.proxy.Close()
	p.proxy = nil
	if err != nil {
		return fmt.Errorf("replay proxy close: %w", err)
	}

	return nil
}

// Report says what a stopped proxy saw that fails a run, "" for nothing: a
// replay miss means a test ran against a 502 rather than a recording, so it
// is said loudly even when the assertions happened to survive it, and a drift
// (collected when verifying alone) means a service still answers, but no
// longer in the shape that was recorded.
func (p *Proxy) Report() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	if len(p.Misses) > 0 {
		fmt.Fprintf(&b, "\nreplay proxy: %d request(s) had no recording:\n", len(p.Misses))
		for _, m := range p.Misses {
			fmt.Fprintln(&b, "  "+m)
		}
		fmt.Fprintf(&b, "record them with %s=1, which records only what is missing\n", p.recordVar)
	}
	if len(p.Drifts) > 0 {
		fmt.Fprintf(&b, "\nreplay proxy: %d response(s) changed shape since recording:\n", len(p.Drifts))
		for _, d := range p.Drifts {
			fmt.Fprintln(&b, "  "+d.String())
		}
		fmt.Fprintf(&b, "\nreview the changes, then record them again with %s=all to accept them\n", p.recordVar)
	}

	return b.String()
}

// ContainerAddresses are the addresses the server reaches itself on: its
// container's addresses and hostname. A server that pings its own address at
// startup sends that through the proxy, because NO_PROXY is set before docker
// hands the container an address - which it does when the container starts,
// so one not yet started has only its hostname. It is nil when the
// environment names no container, or docker cannot say.
func (e Env) ContainerAddresses(ctx context.Context) []string {
	name := e.Container()
	if name == "" {
		return nil
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", //nolint:gosec // the container the test script started, named by the environment it wrote
		"{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}{{.Config.Hostname}}", name).Output()
	if err != nil {
		return nil // not our container to ask about
	}

	return strings.Fields(string(out))
}

// reachScript says what a container sees of the network and probes a port of
// the machine it runs on: $1 is the name it reaches that machine by and $2
// the port. Exit 3 says the image has no probe tool, which is not a failure.
// The hosts entries come too: a container handed an IPv6 route to the host
// gateway can reach the host with one address and not the other.
const reachScript = `grep -iF -- "$1" /etc/hosts; echo "proxy env: ${HTTPS_PROXY:-unset}"; command -v nc >/dev/null || exit 3; nc -z -w 5 "$1" "$2"`

// CheckReachable proves, from inside the container, that the server can reach
// what this machine serves on port - the proxy, a fake indexer - by the name
// it was given for this machine (Host), and says what the container sees of
// the network: its hosts entry for that name and its proxy setting. A server
// that cannot reach the proxy fails every lookup with a timeout of its own,
// which reads as dozens of unrelated assertion failures rather than the one
// plumbing problem it is - so say it plainly, once, before the suite runs,
// naming what could not be reached. With no container named there is nothing
// to check.
func (e Env) CheckReachable(ctx context.Context, what string, port int) (network string, err error) {
	name := e.Container()
	if name == "" {
		return "", nil // not a container this suite started
	}

	host := e.Host()
	out, err := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", reachScript, "sh", host, strconv.Itoa(port)).CombinedOutput() //nolint:gosec // the container the test script started, and a script of this file's own
	network = strings.TrimSpace(string(out))
	switch {
	case err == nil:
		return network, nil
	case strings.Contains(err.Error(), "exit status 3"):
		return network, nil
	default:
		return network, fmt.Errorf("%s cannot reach %s on %s:%d, so every call the server makes to it will time out: %w: %s", name, what, host, port, err, out)
	}
}
