// Package replayproxy is a record/replay HTTP proxy for what a server under
// test fetches from the internet: a media server's metadata providers, a
// podcast feed, an artwork CDN.
//
// A tool that asks its server to look something up never makes the call
// itself: the server, in a container, does. That puts the call out of reach of
// anything that hooks Go's http.RoundTripper, so go-vcr and friends cannot see
// it. The only layer that can is a proxy in front of the container.
//
// The container is started with HTTPS_PROXY naming this proxy and made to
// trust the certificates it signs: a .NET server on Linux trusts whatever
// SSL_CERT_FILE points at, so it is handed the authority in Options.CACert,
// and a Node server can be told not to check (NODE_TLS_REJECT_UNAUTHORIZED=0,
// safe in a throwaway container). In Replay mode - the default, and what CI
// uses - the answers are served from the cassettes on disk and no network is
// touched. Record mode calls the real service for a request no cassette holds
// and writes what comes back, leaving every recording already there alone;
// Rerecord mode refreshes those too, and Verify mode holds each recording
// against what the service answers now without writing anything.
//
// A client in the same process, such as an SDK under test, goes through the
// same proxy with Transport.
package replayproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mode selects whether the proxy calls the real services.
type Mode int

const (
	// Replay serves from the cassettes and never reaches the network. A
	// request with no recording is a loud failure, not an empty response.
	Replay Mode = iota
	// Record calls the real service for a request no cassette holds and
	// writes what comes back; a request already recorded is served from its
	// recording, so recording a new test's lookups leaves every other
	// recording as it was.
	Record
	// Verify calls the real service and compares the shape of what comes
	// back against the cassette, without writing. The recorded response is
	// still what gets served, so a test outcome never depends on what a
	// service happened to return today - drift is reported separately.
	Verify
	// Rerecord is Record refreshing what is recorded as well: the first time
	// the proxy sees a request it calls the real service and the answer
	// replaces the recording, and the same request again in that run is
	// served the fresh answer without another call.
	Rerecord
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// Proxy is a MITM HTTP proxy backed by cassettes.
type Proxy struct {
	mode       Mode
	redact     []string
	redactBody []string
	ignore     []string
	trim       map[string]func([]byte) ([]byte, error)
	recordHint string

	tunnelsMu sync.Mutex
	tunnels   map[string]bool
	store     *store
	listener  net.Listener
	srv       *http.Server
	logger    *log.Logger

	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	certMu sync.Mutex
	certs  map[string]*tls.Certificate

	// upstream reaches the real services, in every mode but Replay
	upstream *http.Transport

	localMu sync.RWMutex
	local   map[string]http.Handler

	missMu sync.Mutex
	misses []string

	driftMu sync.Mutex
	drifts  []Drift

	// fresh is the requests recorded in this proxy's run, which Rerecord
	// serves from their new recording rather than calling for again
	freshMu sync.Mutex
	fresh   map[string]bool
}

// Options configure a Proxy.
type Options struct {
	// Mode defaults to Replay.
	Mode Mode
	// CassetteDir holds one JSON file per host.
	CassetteDir string
	// Addr to listen on. Must be reachable from the container, so bind all
	// interfaces (e.g. ":18080").
	Addr string
	// Logger receives replay misses and record notices; defaults to stderr.
	Logger *log.Logger
	// RecordHint ends the line logged for a request with no recording, saying
	// how this suite records one ("record it with APP_TEST_RECORD=1").
	RecordHint string
	// CACert and CAKey are PEM files holding the certificate authority the
	// proxy signs its per-host certificates with. When both are set the
	// files are loaded (created first if they do not exist), so the same
	// authority can be mounted into a container that was started before the
	// proxy. When empty an in-memory authority is minted for this process.
	CACert, CAKey string
	// RedactQuery names query parameters dropped from every request before
	// it is keyed and recorded: an API key changes from one operator to the
	// next, and a version or an architecture from one machine to the next,
	// and none of them may decide whether a cassette matches, nor be
	// committed with it.
	RedactQuery []string
	// RedactBodyFields names JSON fields whose string value is replaced in a
	// recorded response body. A service's login answers with a bearer token
	// for the server's own account, which is a credential the repository must
	// not carry; replay needs none of it, because the proxy answers the calls
	// that token would authorise.
	RedactBodyFields []string
	// IgnoreHosts are hosts this proxy answers 204 for and never records: the
	// server talking to itself on its own container address, which NO_PROXY
	// cannot exclude because the address is only known once the container is
	// running, or to a service whose answers are no part of what these
	// cassettes are about (a clock, a news feed, a what-is-my-address).
	IgnoreHosts []string
	// Trim cuts a response down before it is recorded, keyed by host and
	// path ("lists.example.org/v1/everything"): a service's list of
	// everything it knows runs to megabytes, of which a suite reads the few
	// entries for its own fixtures. Only a 200 is trimmed. The trimmed body
	// is what is stored and served, in Record and Verify alike, so replay
	// sees the same.
	Trim map[string]func(body []byte) ([]byte, error)
}

// New starts a proxy and returns it. Close stops it and, when it records,
// flushes the cassettes.
func New(opts Options) (*Proxy, error) {
	if opts.CassetteDir == "" {
		return nil, errors.New("replayproxy: CassetteDir is required")
	}
	if opts.Addr == "" {
		opts.Addr = "0.0.0.0:0"
	}
	if opts.Logger == nil {
		opts.Logger = log.New(os.Stderr, "replayproxy: ", 0)
	}

	st, err := newStore(opts.CassetteDir)
	if err != nil {
		return nil, err
	}

	ca, caKey, err := loadOrNewCA(opts.CACert, opts.CAKey)
	if err != nil {
		return nil, err
	}

	p := &Proxy{
		mode:       opts.Mode,
		redact:     opts.RedactQuery,
		redactBody: opts.RedactBodyFields,
		ignore:     opts.IgnoreHosts,
		trim:       opts.Trim,
		recordHint: opts.RecordHint,
		store:      st,
		logger:     opts.Logger,
		ca:         ca,
		caKey:      caKey,
		certs:      map[string]*tls.Certificate{},
		local:      map[string]http.Handler{},
		fresh:      map[string]bool{},
		upstream: &http.Transport{
			Proxy:                 nil, // go straight out; we are the proxy
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   20 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", opts.Addr)
	if err != nil {
		return nil, err
	}
	p.listener = ln
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.dispatch),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		if err := p.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			p.logger.Printf("serve: %v", err)
		}
	}()

	return p, nil
}

// Addr is the address the proxy is listening on.
func (p *Proxy) Addr() string { return p.listener.Addr().String() }

// Transport is for a client in the same process, such as an SDK under test:
// it sends every request through the proxy, on the loopback address
// whatever the proxy listens on, and trusts the certificates the proxy mints.
func (p *Proxy) Transport() *http.Transport {
	pool := x509.NewCertPool()
	pool.AddCert(p.ca)

	return &http.Transport{
		Proxy:           http.ProxyURL(&url.URL{Scheme: schemeHTTP, Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port()))}),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}
}

// Port is the port the proxy is listening on.
func (p *Proxy) Port() int {
	addr, ok := p.listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0
	}

	return addr.Port
}

// Misses returns the requests that had no recording, so a replay run can fail
// with the list rather than leaving tests to pass on empty responses.
func (p *Proxy) Misses() []string {
	p.missMu.Lock()
	defer p.missMu.Unlock()

	return append([]string(nil), p.misses...)
}

// Serve answers every request for host with h instead of a cassette, until
// the returned func is called. It is for the hosts a test plays itself - a
// podcast feed whose episodes it adds as it goes - which have no service to
// record: nothing for such a host is recorded, replayed or counted as a miss.
// The host needs no DNS: the container sends the whole url to the proxy.
//
// A host and a path, written as Trim's keys are ("clock.example.org/v1/time"),
// is that one path alone: an answer no recording can hold, the time of day,
// from a service whose other paths replay as they were recorded.
func (p *Proxy) Serve(target string, h http.Handler) (stop func()) {
	host, path, _ := strings.Cut(target, "/")
	target = strings.ToLower(host)
	if path != "" {
		target += "/" + path
	}

	p.localMu.Lock()
	p.local[target] = h
	p.localMu.Unlock()

	return func() {
		p.localMu.Lock()
		delete(p.local, target)
		p.localMu.Unlock()
	}
}

// localHandler is the handler a test put in front of this path of host, or
// of the whole of host, if any.
func (p *Proxy) localHandler(host, path string) http.Handler {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)

	p.localMu.RLock()
	defer p.localMu.RUnlock()

	if h := p.local[host+path]; h != nil {
		return h
	}

	return p.local[host]
}

// Close stops the proxy, writing any newly recorded cassettes.
func (p *Proxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)

	if p.recording() {
		return p.store.flush()
	}

	return nil
}

// dispatch handles both a CONNECT tunnel (https, which is nearly everything
// a server fetches) and a plain proxied request.
func (p *Proxy) dispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	p.respond(w, r, r.Host)
}

// tunnel answers CONNECT, then terminates TLS itself with a certificate minted
// for the requested host, and serves the requests inside.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}

	p.sawTunnel(host)

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	raw, _, err := hj.Hijack()
	if err != nil {
		p.logger.Printf("hijack: %v", err)
		return
	}
	defer func() { _ = raw.Close() }()

	if _, err := raw.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	cert, err := p.certFor(host)
	if err != nil {
		p.logger.Printf("cert for %s: %s", logSafe(host), logSafe(err.Error()))
		return
	}
	conn := tls.Server(raw, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
	})
	// a handshake with no deadline can hang forever on a client that opened
	// the tunnel and then sent nothing, which is silence in the log exactly
	// where an answer is needed
	if err := raw.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		p.logger.Printf("deadline for %s: %s", logSafe(host), logSafe(err.Error()))
		return
	}
	if err := conn.HandshakeContext(r.Context()); err != nil {
		// the client hung up or refused our certificate; in a container set
		// up to trust it the latter should not happen, so say so rather than
		// leave a server timing out against a silent proxy
		p.logger.Printf("tls handshake with %s: %s", logSafe(host), logSafe(err.Error()))
		return
	}
	defer func() { _ = conn.Close() }()
	defer dropReader(conn)

	if err := raw.SetDeadline(time.Time{}); err != nil {
		p.logger.Printf("clearing the deadline for %s: %s", logSafe(host), logSafe(err.Error()))
		return
	}

	// serve every request on the tunnel until the peer closes it
	served := 0
	for {
		if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
			return
		}
		req, err := http.ReadRequest(newReader(conn))
		if err != nil {
			// EOF is the peer closing a finished tunnel; anything else, on a
			// tunnel that carried nothing, is worth saying out loud
			if served == 0 {
				p.logger.Printf("tunnel to %s carried no request: %s", logSafe(host), logSafe(err.Error()))
			}

			return
		}
		served++
		rec := &connResponse{conn: conn}
		p.respond(rec, req, host)
		if rec.closed || rec.last || req.Close {
			return
		}
	}
}

// respond serves one request from the cassettes, recording it first when in
// Record mode and it has no recording, or in Rerecord mode and this run has
// not recorded it yet.
func (p *Proxy) respond(w http.ResponseWriter, r *http.Request, host string) {
	if r.Body != nil {
		defer func() { _ = r.Body.Close() }()
	}
	if h := p.localHandler(host, r.URL.Path); h != nil {
		// buffered, so it goes out with a length: inside a tunnel nothing
		// else would tell the client where the body ends
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		maps.Copy(w.Header(), rec.Header())
		w.Header().Set("Content-Length", strconv.Itoa(rec.Body.Len()))
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())

		return
	}
	if p.ignored(host) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	p.redactQuery(r)
	k := key(r.Method, host, path, r.URL.Query())

	if i, ok := p.store.lookup(k); ok && !p.stale(k) {
		// a credential the recording blanked out is no credential: handed
		// back while recording, every call the server makes with it that no
		// cassette holds goes out unauthorised, and its refusal is what gets
		// recorded. The server is handed a live one, and the recording kept
		if p.recording() && i.redacted() {
			live, err := p.fetch(r, host, k, path)
			if err == nil {
				p.logger.Printf("live %s -> %d (its recording holds a redacted credential)", logSafe(k), live.Status)
				writeInteraction(w, live)
				return
			}
			p.logger.Printf("live %s: %s, replaying the recording", logSafe(k), logSafe(err.Error()))
		}
		p.logger.Printf("replay %s -> %d", logSafe(k), i.Status)
		if p.mode == Verify {
			live, err := p.fetch(r, host, k, path)
			if err != nil {
				p.logger.Printf("verify %s: %s", logSafe(k), logSafe(err.Error()))
			} else {
				// held against the recording as the recording holds it
				live.redact(p.redactBody)
				p.compare(i, live)
			}
		}
		writeInteraction(w, i)
		return
	}

	if p.mode == Replay || p.mode == Verify {
		p.missMu.Lock()
		p.misses = append(p.misses, k)
		p.missMu.Unlock()
		if p.recordHint != "" {
			p.logger.Printf("REPLAY MISS %s (%s)", logSafe(k), p.recordHint)
		} else {
			p.logger.Printf("REPLAY MISS %s", logSafe(k))
		}
		http.Error(w, "replayproxy: no recording for "+k, http.StatusBadGateway)
		return
	}

	i, err := p.record(r, host, k, path)
	if err != nil {
		p.logger.Printf("record %s: %s", logSafe(k), logSafe(err.Error()))
		http.Error(w, "replayproxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeInteraction(w, i)
}

// logSafe keeps a value taken off a request to one line of the log: a
// newline in a url or a host would start a line the proxy never wrote. The
// reason a request failed goes through it too, because it names the request.
func logSafe(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	return strings.ReplaceAll(s, "\r", "")
}

// ignored reports whether host is one the proxy answers for without a
// cassette: the server reaching itself, or a service no recording is about.
func (p *Proxy) ignored(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = host
	}
	for _, h := range p.ignore {
		if strings.EqualFold(h, host) || strings.EqualFold(h, name) {
			return true
		}
	}

	return false
}

// sawTunnel logs the first CONNECT for a host, so a run that records or
// replays nothing can be told apart from one whose requests never arrived.
func (p *Proxy) sawTunnel(host string) {
	p.tunnelsMu.Lock()
	defer p.tunnelsMu.Unlock()

	if p.tunnels == nil {
		p.tunnels = map[string]bool{}
	}
	if p.tunnels[host] {
		return
	}
	p.tunnels[host] = true
	p.logger.Printf("tunnel to %s", logSafe(host))
}

// redactQuery strips the RedactQuery parameters from the request, so they are
// neither keyed on nor written to a cassette. The real service still needs
// them, so the values are kept aside and put back by fetch.
func (p *Proxy) redactQuery(r *http.Request) {
	if len(p.redact) == 0 {
		return
	}
	q := r.URL.Query()
	kept := url.Values{}
	for _, name := range p.redact {
		if vals, ok := q[name]; ok {
			kept[name] = vals
			q.Del(name)
		}
	}
	if len(kept) == 0 {
		return
	}
	r.URL.RawQuery = q.Encode()
	if r.Header == nil {
		r.Header = http.Header{}
	}
	r.Header.Set(redactedHeader, kept.Encode())
}

// redactedHeader carries the stripped parameters from redactQuery to fetch,
// on the request itself so nothing else has to know.
const redactedHeader = "X-Replayproxy-Redacted"

// fetch calls the real service and returns what it sent back, without
// storing it.
func (p *Proxy) fetch(r *http.Request, host, k, path string) (*interaction, error) {
	target := &url.URL{Scheme: schemeHTTPS, Host: host, Path: path, RawQuery: r.URL.RawQuery}
	if r.TLS == nil && r.URL.Scheme == schemeHTTP {
		target.Scheme = schemeHTTP
	}
	if kept := r.Header.Get(redactedHeader); kept != "" {
		r.Header.Del(redactedHeader)
		if target.RawQuery == "" {
			target.RawQuery = kept
		} else {
			target.RawQuery += "&" + kept
		}
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return nil, err
	}
	for name, vals := range r.Header {
		// Accept-Encoding is left to the transport, which then asks for gzip
		// alone and decodes it, dropping Content-Encoding: the cassette holds
		// the answer as text a diff, a grep and Verify can read, a credential
		// in it can be redacted, and no service is offered an encoding
		// nothing here could decode
		if strings.EqualFold(name, "Proxy-Connection") || strings.EqualFold(name, "Accept-Encoding") {
			continue
		}
		for _, v := range vals {
			outReq.Header.Add(name, v)
		}
	}
	outReq.Host = host

	resp, err := p.upstream.RoundTrip(outReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if trim := p.trim[strings.ToLower(host)+path]; trim != nil && resp.StatusCode == http.StatusOK {
		if body, err = trim(body); err != nil {
			return nil, fmt.Errorf("trimming %s%s: %w", host, path, err)
		}
	}

	i := &interaction{
		Key:     k,
		Method:  strings.ToUpper(r.Method),
		Host:    strings.ToLower(host),
		Path:    path,
		Query:   r.URL.RawQuery,
		Status:  resp.StatusCode,
		Headers: keepHeaders(resp.Header),
	}
	i.setBody(body, resp.Header.Get("Content-Type"))

	return i, nil
}

// record fetches and stores, replacing any recording of the same request.
// fetch alone is what Verify uses, so that a verification run never writes to
// the cassettes.
//
// What is stored is redacted and what is answered is not: the server is the
// one that asked, and a login it is handed with its token blanked out is a
// login it cannot use. One service answered every call after a login recorded
// that way with a 401, and those 401s were what went into the cassettes.
// Replay hands the redacted token back, which is harmless there, because
// every call it would authorise is answered from the recording; a recording
// run fetches it again instead (respond).
//
// A rate limit or a server error is passed on but not stored: it says
// nothing about the service's answer, and a recording of one would be
// replayed as if it did.
func (p *Proxy) record(r *http.Request, host, k, path string) (*interaction, error) {
	i, err := p.fetch(r, host, k, path)
	if err != nil {
		return nil, err
	}
	if i.Status == http.StatusTooManyRequests || i.Status >= http.StatusInternalServerError {
		p.logger.Printf("not recording %s: the service answered %d", logSafe(k), i.Status)

		return i, nil
	}
	stored := i.clone()
	stored.redact(p.redactBody)
	p.store.put(stored.Host, stored)
	p.freshMu.Lock()
	p.fresh[k] = true
	p.freshMu.Unlock()
	p.logger.Printf("recorded %s -> %d", logSafe(k), i.Status)

	return i, nil
}

// recording reports whether this run writes to the cassettes.
func (p *Proxy) recording() bool { return p.mode == Record || p.mode == Rerecord }

// stale reports whether a recorded request is to be called for again rather
// than replayed: in Rerecord mode, until this run has recorded it.
func (p *Proxy) stale(k string) bool {
	if p.mode != Rerecord {
		return false
	}
	p.freshMu.Lock()
	defer p.freshMu.Unlock()

	return !p.fresh[k]
}

func writeInteraction(w http.ResponseWriter, i *interaction) {
	body := i.bytes()
	for name, v := range i.Headers {
		w.Header().Set(name, v)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(i.Status)
	_, _ = w.Write(body) //nolint:gosec // a service's answer relayed to the server that asked for it, which is what the proxy is for
}

// loadOrNewCA returns the authority in the given PEM files, minting and
// writing it when the files are missing, or an in-memory one when no files
// are named.
func loadOrNewCA(certFile, keyFile string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	if certFile == "" || keyFile == "" {
		return newCA()
	}

	certPEM, certErr := os.ReadFile(certFile) //nolint:gosec // the CA path is the test harness's own
	keyPEM, keyErr := os.ReadFile(keyFile)    //nolint:gosec // the CA path is the test harness's own
	if certErr == nil && keyErr == nil {
		return parseCA(certPEM, keyPEM)
	}
	if !os.IsNotExist(certErr) && certErr != nil {
		return nil, nil, certErr
	}
	if !os.IsNotExist(keyErr) && keyErr != nil {
		return nil, nil, keyErr
	}

	ca, key, err := newCA()
	if err != nil {
		return nil, nil, err
	}
	if err := writeCA(certFile, keyFile, ca, key); err != nil {
		return nil, nil, err
	}

	return ca, key, nil
}

// parseCA decodes a PEM certificate and its EC private key.
func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, errors.New("replayproxy: CA cert file holds no CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, errors.New("replayproxy: CA key file holds no PEM block")
	}
	var key *ecdsa.PrivateKey
	switch kb.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(kb.Bytes)
	case "PRIVATE KEY":
		var k any
		k, err = x509.ParsePKCS8PrivateKey(kb.Bytes)
		if err == nil {
			var ok bool
			if key, ok = k.(*ecdsa.PrivateKey); !ok {
				err = errors.New("replayproxy: CA key is not an EC key")
			}
		}
	default:
		err = errors.New("replayproxy: CA key file holds a " + kb.Type + " block, want EC PRIVATE KEY")
	}
	if err != nil {
		return nil, nil, err
	}

	return cert, key, nil
}

// writeCA persists a minted authority so a container started later can
// trust it.
func writeCA(certFile, keyFile string, ca *x509.Certificate, key *ecdsa.PrivateKey) error {
	if err := os.MkdirAll(filepath.Dir(certFile), 0o750); err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	// the container reads the certificate as an unprivileged user, so it is
	// world-readable; the key stays private to us
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0o644); err != nil { //nolint:gosec // a public certificate
		return err
	}

	return os.WriteFile(keyFile, keyPEM, 0o600)
}

// newCA mints the in-memory authority that signs the per-host certificates.
func newCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "go-kt replay proxy CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(30 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}

	return cert, k, nil
}

// certFor mints (and caches) a leaf certificate for one hostname.
func (p *Proxy) certFor(host string) (*tls.Certificate, error) {
	p.certMu.Lock()
	defer p.certMu.Unlock()

	if c, ok := p.certs[host]; ok {
		return c, nil
	}

	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &k.PublicKey, p.caKey)
	if err != nil {
		return nil, err
	}
	cert := &tls.Certificate{Certificate: [][]byte{der, p.ca.Raw}, PrivateKey: k}
	p.certs[host] = cert

	return cert, nil
}
