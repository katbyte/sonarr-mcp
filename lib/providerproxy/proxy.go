// Package providerproxy is a record/replay HTTP proxy for the services
// Sonarr calls out to: SkyHook (skyhook.sonarr.tv), its front to TheTVDB for
// every series lookup and refresh, Sonarr's own services.sonarr.tv, and the
// artwork CDN.
//
// sonarr-mcp never talks to those itself: it asks Sonarr to (look a series
// up, add it, refresh it), and Sonarr - a .NET app in a container - makes the
// calls. That puts them out of reach of anything that hooks Go's
// http.RoundTripper, so go-vcr and friends cannot see them. The only layer
// that can is a proxy in front of the container.
//
// .NET honours HTTPS_PROXY, and on Linux it trusts whatever SSL_CERT_FILE
// points at, so the container is started with both: the proxy address, and
// a CA certificate the proxy signs its per-host certificates with (Options.CA;
// scripts/testenv.sh mints it and mounts it into the container). In record
// mode the real providers are called once and the responses are written to
// cassettes; in replay mode - the default, and what CI uses - they are served
// from disk and no network is touched.
package providerproxy

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
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mode selects whether the proxy calls the real providers.
type Mode int

const (
	// Replay serves from the cassettes and never reaches the network. A
	// request with no recording is a loud failure, not an empty response.
	Replay Mode = iota
	// Record calls the real provider and writes what comes back.
	Record
	// Verify calls the real provider and compares the shape of what comes
	// back against the cassette, without writing. The recorded response is
	// still what gets served, so a test outcome never depends on what a
	// provider happened to return today - drift is reported separately.
	Verify
)

// Proxy is a MITM HTTP proxy backed by cassettes.
type Proxy struct {
	mode       Mode
	redact     []string
	redactBody []string
	ignore     []string

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

	// upstream is used in Record mode only
	upstream *http.Transport

	missMu sync.Mutex
	misses []string

	driftMu sync.Mutex
	drifts  []Drift
}

// Options configure a Proxy.
type Options struct {
	// Mode defaults to Replay.
	Mode Mode
	// CassetteDir holds one JSON file per provider host.
	CassetteDir string
	// Addr to listen on. Must be reachable from the container, so bind all
	// interfaces (e.g. "0.0.0.0:18080").
	Addr string
	// Logger receives replay misses and record notices; defaults to stderr.
	Logger *log.Logger
	// CACert and CAKey are PEM files holding the certificate authority the
	// proxy signs its per-host certificates with. When both are set the
	// files are loaded (created first if they do not exist), so the same
	// authority can be mounted into a container that was started before the
	// proxy. When empty an in-memory authority is minted for this process.
	CACert, CAKey string
	// RedactQuery names query parameters dropped from every request before
	// it is keyed and recorded: an API key such as TMDB's api_key changes
	// from one operator to the next and must not decide whether a cassette
	// matches, nor be committed with it.
	RedactQuery []string
	// RedactBodyFields names JSON fields whose string value is replaced in a
	// recorded response body: a credential a provider hands the server, which
	// the repository must not carry; replay needs none of it, because the
	// proxy answers the calls it would authorise.
	RedactBodyFields []string
	// IgnoreHosts are hosts this proxy answers 204 for and never records: the
	// server talking to itself on its own container address, which NO_PROXY
	// cannot exclude because the address is only known once the container is
	// running, and which is no part of what these cassettes are about.
	IgnoreHosts []string
}

// New starts a proxy and returns it. Close stops it and, in Record mode,
// flushes the cassettes.
func New(opts Options) (*Proxy, error) {
	if opts.CassetteDir == "" {
		return nil, errors.New("providerproxy: CassetteDir is required")
	}
	if opts.Addr == "" {
		opts.Addr = "0.0.0.0:0"
	}
	if opts.Logger == nil {
		opts.Logger = log.New(os.Stderr, "providerproxy: ", 0)
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
		store:      st,
		logger:     opts.Logger,
		ca:         ca,
		caKey:      caKey,
		certs:      map[string]*tls.Certificate{},
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
		Handler:           http.HandlerFunc(p.serve),
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

// Close stops the proxy, writing any newly recorded cassettes.
func (p *Proxy) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)

	if p.mode == Record {
		return p.store.flush()
	}

	return nil
}

// serve handles both a CONNECT tunnel (https, which is everything the
// providers use) and a plain proxied request.
func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
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
		p.logger.Printf("cert for %s: %v", host, err)
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
		p.logger.Printf("deadline for %s: %v", host, err)
		return
	}
	if err := conn.HandshakeContext(r.Context()); err != nil {
		// the client hung up or refused our certificate; with SSL_CERT_FILE
		// pointing at our CA the latter should not happen, so say so rather
		// than leave a server timing out against a silent proxy
		p.logger.Printf("tls handshake with %s: %v", host, err)
		return
	}
	defer func() { _ = conn.Close() }()

	if err := raw.SetDeadline(time.Time{}); err != nil {
		p.logger.Printf("clearing the deadline for %s: %v", host, err)
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
				p.logger.Printf("tunnel to %s carried no request: %v", host, err)
			}

			return
		}
		served++
		rec := &connResponse{conn: conn}
		p.respond(rec, req, host)
		if rec.closed || req.Close {
			return
		}
	}
}

// respond serves one request from the cassettes, recording it first when in
// Record mode.
func (p *Proxy) respond(w http.ResponseWriter, r *http.Request, host string) {
	if r.Body != nil {
		defer func() { _ = r.Body.Close() }()
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

	if i, ok := p.store.lookup(k); ok {
		p.logger.Printf("replay %s -> %d", k, i.Status)
		if p.mode == Verify {
			live, err := p.fetch(r, host, k, path)
			if err != nil {
				p.logger.Printf("verify %s: %v", k, err)
			} else {
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
		p.logger.Printf("REPLAY MISS %s (run `make record` to capture it)", k)
		http.Error(w, "providerproxy: no recording for "+k, http.StatusBadGateway)
		return
	}

	i, err := p.record(r, host, k, path)
	if err != nil {
		p.logger.Printf("record %s: %v", k, err)
		http.Error(w, "providerproxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	p.logger.Printf("recorded %s -> %d", k, i.Status)
	writeInteraction(w, i)
}

// ignored reports whether host is one the proxy answers for without a
// cassette: the server reaching itself, which is not provider traffic.
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
	p.logger.Printf("tunnel to %s", host)
}

// redactQuery strips the RedactQuery parameters from the request, so they are
// neither keyed on nor written to a cassette. The real provider still needs
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
const redactedHeader = "X-Providerproxy-Redacted"

// fetch calls the real provider and returns what it sent back, without
// storing it.
func (p *Proxy) fetch(r *http.Request, host, k, path string) (*interaction, error) {
	target := &url.URL{Scheme: "https", Host: host, Path: path, RawQuery: r.URL.RawQuery}
	if r.TLS == nil && r.URL.Scheme == "http" {
		target.Scheme = "http"
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
		// Accept-Encoding is left to the transport, which asks for gzip and
		// decodes it: passing on a client's br or deflate would record a
		// compressed body, unreadable in a diff and undecodable by the
		// shape check in Verify
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
	i.Body = redactJSONFields(i.Body, p.redactBody)

	return i, nil
}

// record fetches and stores. fetch alone is what Verify uses, so that a
// verification run never writes to the cassettes.
func (p *Proxy) record(r *http.Request, host, k, path string) (*interaction, error) {
	i, err := p.fetch(r, host, k, path)
	if err != nil {
		return nil, err
	}
	p.store.put(i.Host, i)

	return i, nil
}

func writeInteraction(w http.ResponseWriter, i *interaction) {
	body := i.bytes()
	for name, v := range i.Headers {
		w.Header().Set(name, v)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(i.Status)
	_, _ = w.Write(body)
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
		return nil, nil, errors.New("providerproxy: CA cert file holds no CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, errors.New("providerproxy: CA key file holds no PEM block")
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
				err = errors.New("providerproxy: CA key is not an EC key")
			}
		}
	default:
		err = errors.New("providerproxy: CA key file holds a " + kb.Type + " block, want EC PRIVATE KEY")
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
		Subject:               pkix.Name{CommonName: "sonarr-mcp provider proxy CA"},
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

// certFor mints (and caches) a leaf certificate for one provider hostname.
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
