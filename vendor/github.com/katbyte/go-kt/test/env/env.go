// Package env is what a suite against a live server in a container is handed: the variables its test script exports, the replay proxy the server's
// internet calls go through, a check the container can reach it, the report on what the proxy saw, and files laid out where the container reads them.
// Every variable carries the app's prefix: New("APP") reads APP_SERVER, APP_TOKEN and the rest.
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

// DefaultProxyPort is the port the proxy listens on when the environment names none.
const DefaultProxyPort = 18080

// Env reads one app's test environment.
type Env struct {
	prefix string
	getenv func(string) string
}

// New is the environment under prefix, without its underscore: "APP" for APP_SERVER.
func New(prefix string) Env {
	return Env{prefix: strings.TrimSuffix(prefix, "_"), getenv: os.Getenv}
}

// Var is the full name of one of the app's variables: Var("TEST_DATA") is APP_TEST_DATA.
func (e Env) Var(name string) string { return e.prefix + "_" + name }

// Get is the value of one of the app's variables, "" when it is not set.
func (e Env) Get(name string) string { return e.getenv(e.Var(name)) }

// Server is the address of the server under test (APP_SERVER).
func (e Env) Server() string { return e.Get("SERVER") }

// Token is the credential for it (APP_TOKEN).
func (e Env) Token() string { return e.Get("TOKEN") }

// Configured reports whether the container environment is present: a server and a token, and each of the other variables a suite cannot run without.
func (e Env) Configured(also ...string) bool {
	for _, name := range append([]string{"SERVER", "TOKEN"}, also...) {
		if e.Get(name) == "" {
			return false
		}
	}

	return true
}

// Recording reports whether this run may call the real services: APP_TEST_RECORD=1 records only what is missing, =all records everything afresh.
func (e Env) Recording() bool { return e.Get("TEST_RECORD") != "" }

// Verifying reports whether to check the cassettes against the live services without rewriting them (APP_TEST_VERIFY).
func (e Env) Verifying() bool { return e.Get("TEST_VERIFY") != "" }

// Mode is how the environment asks the proxy to run: replaying unless told to record or to verify, and recording when told both.
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

// DataDir is the host folder the container's data is mounted from (APP_TEST_DATA) with elem joined on, so a test can lay files out for the server. ""
// when unset, which a test must skip on or it writes into the checkout.
func (e Env) DataDir(elem ...string) string {
	root := e.Get("TEST_DATA")
	if root == "" {
		return ""
	}

	return filepath.Join(append([]string{root}, elem...)...)
}

// ProxyPort is the port the proxy listens on, APP_TEST_PROXY_PORT or DefaultProxyPort, which the container's HTTPS_PROXY already names.
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

// Container is the name of the container the test script started (APP_TEST_CONTAINER), "" when the server under test is not one of ours.
func (e Env) Container() string { return e.Get("TEST_CONTAINER") }

// DefaultHost is the name a container reaches the machine it runs on by when the environment names none.
const DefaultHost = "host.docker.internal"

// Host is the name the container reaches this machine by (APP_TEST_HOST), DefaultHost when unset; on Linux a script may give the bridge gateway's
// address instead.
func (e Env) Host() string {
	if h := e.Get("TEST_HOST"); h != "" {
		return h
	}

	return DefaultHost
}

// Proxy is the replay proxy a suite runs for its whole run, and what it saw once stopped.
type Proxy struct {
	proxy     *replayproxy.Proxy
	recordVar string
	// advice is what the report says to do about a miss when not to record it; unrecorded is a suite with no recordings at all
	advice     string
	unrecorded bool
	// Misses are the requests with no recording, Drifts the answers that changed shape since recording; both read when the proxy stops
	Misses []string
	Drifts []replayproxy.Drift
}

// StartProxy brings up the proxy the container's HTTPS_PROXY points at (ListenProxy) and proves the container can reach it. The container must be
// running; a suite that starts it after the proxy calls ListenProxy, starts it, then CheckReachable.
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

// ListenProxy brings the proxy up without asking the container anything, for a server that calls out as it starts. The environment sets the mode, the
// address (every interface on ProxyPort), the authority under APP_TEST_PROXY_CA, and the server's own addresses to ignore; the rest is the suite's.
// With no CassetteDir the suite keeps no recordings and a miss is told so rather than told to record.
func (e Env) ListenProxy(ctx context.Context, opts replayproxy.Options) (*Proxy, error) {
	// the advice for a miss is to record, unless the suite said otherwise or has nowhere to record to
	advised := opts.RecordHint != "" || opts.CassetteDir == ""
	opts, err := e.proxyOptions(ctx, opts)
	if err != nil {
		return nil, err
	}

	p, err := replayproxy.New(opts) //nolint:contextcheck // the proxy outlives this call: it runs for the whole suite
	if err != nil {
		return nil, err
	}

	proxy := &Proxy{proxy: p, recordVar: e.Var("TEST_RECORD"), unrecorded: opts.CassetteDir == ""}
	if advised {
		proxy.advice = opts.RecordHint
	}

	return proxy, nil
}

// proxyOptions is a suite's options with what the environment decides filled in.
func (e Env) proxyOptions(ctx context.Context, opts replayproxy.Options) (replayproxy.Options, error) {
	port, err := e.ProxyPort()
	if err != nil {
		return opts, err
	}

	opts.Mode = e.Mode()
	if opts.Addr == "" {
		// every interface and both stacks: a container with an IPv6 route would find nothing on an IPv4-only socket
		opts.Addr = ":" + strconv.Itoa(port)
	}
	if ca := e.Get("TEST_PROXY_CA"); ca != "" && opts.CACert == "" && opts.CAKey == "" {
		opts.CACert, opts.CAKey = filepath.Join(ca, "ca.pem"), filepath.Join(ca, "ca.key")
	}
	switch {
	case opts.RecordHint != "":
	case opts.CassetteDir == "":
		// nothing to record: a request nobody answers is one to answer
		opts.RecordHint = noRecordingsHint
	default:
		opts.RecordHint = "record it with " + e.Var("TEST_RECORD") + "=1, which records only what is missing"
	}
	// the server reaching itself is nothing a recording is about
	opts.IgnoreHosts = append(append([]string(nil), opts.IgnoreHosts...), e.ContainerAddresses(ctx)...)

	return opts, nil
}

// noRecordingsHint is what a miss is told when the suite has no cassettes.
const noRecordingsHint = "this suite keeps no recordings: answer it from the suite, or find what started asking for it"

// Addr is the address the proxy listens on.
func (p *Proxy) Addr() string { return p.proxy.Addr() }

// Transport is for a client in the test's own process to go through the proxy too (replayproxy.Proxy.Transport).
func (p *Proxy) Transport() *http.Transport { return p.proxy.Transport() }

// Serve answers a host, or one path of it, with h rather than a recording until stop is called (replayproxy.Proxy.Serve).
func (p *Proxy) Serve(target string, h http.Handler) (stop func()) { return p.proxy.Serve(target, h) }

// Stop closes the proxy and keeps what it saw; stopping twice is harmless.
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

// Report says what a stopped proxy saw that fails a run, "" for nothing: a miss, which means a test ran against a 502, with what to do about it; and
// a drift, a service answering in a new shape.
func (p *Proxy) Report() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	if len(p.Misses) > 0 {
		lacked := "recording"
		if p.unrecorded {
			lacked = "answer"
		}
		fmt.Fprintf(&b, "\nreplay proxy: %d request(s) had no %s:\n", len(p.Misses), lacked)
		for _, m := range p.Misses {
			fmt.Fprintln(&b, "  "+m)
		}
		if p.advice != "" {
			fmt.Fprintln(&b, p.advice)
		} else {
			fmt.Fprintf(&b, "record them with %s=1, which records only what is missing\n", p.recordVar)
		}
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

// ContainerAddresses are the addresses the server reaches itself on, which go through the proxy because NO_PROXY is set before docker hands them out;
// nil with no container, or when docker cannot say.
func (e Env) ContainerAddresses(ctx context.Context) []string {
	name := e.Container()
	if name == "" {
		return nil
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}{{.Config.Hostname}}", name).Output() //nolint:gosec // the container the test script started, named by the environment it wrote
	if err != nil {
		return nil // not our container to ask about
	}

	return strings.Fields(string(out))
}

// reachScript says what a container sees of the network and probes a port of its host: $1 the host's name, $2 the port. Exit 3 is an image with no
// probe tool, which is not a failure.
const reachScript = `grep -iF -- "$1" /etc/hosts; echo "proxy env: ${HTTPS_PROXY:-unset}"; command -v nc >/dev/null || exit 3; nc -z -w 5 "$1" "$2"`

// CheckReachable proves from inside the container that it can reach what this machine serves on port, by Host, and says what it sees of the network.
// A server that cannot reach the proxy fails every lookup, which reads as dozens of unrelated failures, so this says it once, plainly.
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
