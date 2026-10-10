// Package chttp provides the HTTP client every katbyte tool talks to APIs
// with: per-attempt timeouts so a stalled server fails fast, retries for
// transient failures that are careful never to re-send a mutation whose
// outcome is unknown, and a trace of every exchange for whoever hands it a
// logger.
//
// It imports nothing outside the standard library and logs nothing of its
// own accord, so an SDK can be built on it without handing whoever uses that
// SDK a logger they did not ask for. The application that wants the traffic
// in its log says so when it makes the client:
//
//	client := chttp.New(chttp.Options{Name: "Audiobookshelf", Log: clog.Log})
//
// What counts as worth another try, how long to wait for an answer and what
// is too secret to print are each API's own, so each is the caller's to set
// (Options); the defaults are the cautious ones.
package chttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultTries is how many times a request is sent before giving up: enough
// to ride through a blip or a single rate-limit window without turning an
// outage into a minutes-long hang.
const DefaultTries = 3

// DefaultHeaderWait is how long a server has to start answering.
const DefaultHeaderWait = 30 * time.Second

// MaxRetryAfter caps how long a Retry-After header can make a retry wait. A
// server asking for more than this is telling an interactive tool to come back
// later, not to sit there.
const MaxRetryAfter = 60 * time.Second

// Logger is where a client says what it is doing: a retry at debug, and each
// request and answer at trace. A logrus logger is one as it stands, clog.Log
// among them.
//
// What is traced is put together only when the logger formats it, and is
// read from the request and the answer at that moment. So a logger must
// format before Tracef returns, and one that drops a level without
// formatting, as logrus does, costs nothing while that level is off.
type Logger interface {
	Debugf(format string, args ...any)
	Tracef(format string, args ...any)
}

// Options are what a client is made with. The zero value is a client that
// logs nothing and retries with the defaults.
type Options struct {
	// Name says whose traffic this is in a log, so a tool talking to two
	// APIs can tell them apart.
	Name string
	// Log is where retries and the trace of each exchange go. Nil logs
	// nothing.
	Log Logger
	// Retry is when a request is sent again.
	Retry Retry
	// HeaderWait is how long the server has to start answering one
	// attempt; 0 is DefaultHeaderWait. An API with calls that are slow to
	// start answering - a search it runs before it writes a byte - wants
	// longer. Reading the answer is not bounded by it.
	HeaderWait time.Duration
	// SecretHeaders are headers whose values a trace must not show, beside
	// Authorization, Proxy-Authorization, Cookie and Set-Cookie: the header
	// of its own an API takes a key in.
	SecretHeaders []string
	// SecretNames are the names whose values a trace must not show wherever
	// they appear - a query parameter, a form field, a JSON field - compared
	// without case. They are beside the ones a trace hides unasked: any name
	// that ends in password, secret, token, apikey or api_key, so
	// proxyPassword, refresh_token and TmdbApiKey need no naming here and an
	// API's "pass" or "pin" does.
	SecretNames []string
	// TraceBody is how much of a body a trace prints, in bytes: 0 is
	// DefaultTraceBody, and a negative number prints no body at all. No
	// more than this is ever read to print it.
	TraceBody int
	// Base is what requests are sent through underneath the retries and
	// the trace; nil is NewBaseTransport. A test hands in its own, and so
	// does an application with a proxy or certificates of its own.
	Base http.RoundTripper
}

// Retry is when a request is sent again, and how long after. The zero value
// is the default: three tries a second and then two apart, for an answer of
// 502, 503 or 504 and for a connection that dropped.
//
// A request is only ever sent again when that cannot do the work twice. One
// refused with 429 was turned away before it was acted on, so any request
// may be; otherwise only a request that is safe to repeat is - a GET, HEAD
// or OPTIONS, or one marked with MarkRetrySafe. A write that gets no answer,
// or an error for one, may still have been made.
type Retry struct {
	// Tries is the most a request is ever sent; 0 is DefaultTries and 1
	// never sends it again. It is one number however the request fails: a
	// read that Fetch asks for again does not start the count afresh.
	Tries int
	// Wait is how long to wait after the attempt numbered from 0 failed;
	// nil is 1s, 2s, 4s and so on. A 429's Retry-After is used in its
	// place, up to MaxRetryAfter.
	Wait func(attempt int) time.Duration
	// Status says whether an answer with this status is worth another try;
	// nil is 502, 503 and 504, which a gateway gives for a server it could
	// not reach. A plain 500 is left out: many servers answer it for what
	// will never succeed, and trying those again only makes the failure
	// slower. An API that uses 500 for "busy" adds it here.
	Status func(code int) bool
	// Error says whether a request that got no answer is worth sending
	// again; nil is Dropped, a connection that was there and went. A server
	// that cannot be reached at all is not one, and neither is a timeout:
	// waiting the same wait again is rarely what is wanted.
	Error func(err error) bool
}

func (r Retry) tries() int {
	if r.Tries == 0 {
		return DefaultTries
	}

	return max(1, r.Tries)
}

func (r Retry) wait(attempt int) time.Duration {
	if r.Wait != nil {
		return r.Wait(attempt)
	}

	return backoff(attempt)
}

func (r Retry) status(code int) bool {
	if r.Status != nil {
		return r.Status(code)
	}

	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

func (r Retry) failed(err error) bool {
	if r.Error != nil {
		return r.Error(err)
	}

	return Dropped(err)
}

type ctxKey int

const (
	retrySafeKey ctxKey = iota
	triesKey
)

// MarkRetrySafe declares a request safe to re-send even though its method is
// not idempotent: a GraphQL or JQL query is a read that happens to travel as a
// POST. Reads with idempotent methods (GET, HEAD, OPTIONS) need no mark.
func MarkRetrySafe(req *http.Request) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), retrySafeKey, true))
}

// retrySafe reports whether a request may be re-sent when its outcome is
// unknown (no answer, or a server error): true for idempotent methods and
// marked reads. A mutation that gets no response may still have been applied
// server-side, so re-sending it risks doing the work twice.
func retrySafe(req *http.Request) bool {
	if v, ok := req.Context().Value(retrySafeKey).(bool); ok && v {
		return true
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}

	return false
}

// counted gives a request a count of the times it has been sent again,
// unless it has one already: Fetch starts the count, so that the times it
// asks again and the times the transport does under it are one number, held
// to one limit. It counts sending again and not sending, so that a redirect
// followed on the way, which is another request and not another try, costs
// nothing.
func counted(req *http.Request) (with *http.Request, again *int) {
	if n, ok := req.Context().Value(triesKey).(*int); ok {
		return req, n
	}
	n := new(int)

	return req.WithContext(context.WithValue(req.Context(), triesKey, n)), n
}

// Tries is how many times the request behind an answer was sent, for an
// error that says so (StatusError's Tries): 1 for an answer that came first
// time, and 0 for one no client made here sent.
func Tries(resp *http.Response) int {
	if resp == nil || resp.Request == nil {
		return 0
	}
	if n, ok := resp.Request.Context().Value(triesKey).(*int); ok {
		return *n + 1
	}

	return 0
}

// Client is an http.Client whose requests are tried again and traced as its
// Options say. It is used as one - Do, Timeout and CheckRedirect are all
// there - and adds Fetch, for an answer read whole.
type Client struct {
	*http.Client

	o Options
}

// New returns a client made as the options say. It sets no limit on a whole
// request and follows redirects as Go does: set Timeout and CheckRedirect on
// it for an API that wants otherwise (see RefuseRedirects and
// KeepCredentialsOnHost).
//
// Timeout, when set, is for everything one Do does: every try it makes and
// the waits between them, since the tries are made underneath it. Each time
// Fetch asks again is a Do of its own, with the whole Timeout to itself.
func New(o Options) *Client {
	base := o.Base
	if base == nil {
		base = NewBaseTransport(o)
	}

	return &Client{Client: &http.Client{Transport: NewRetryTransport(o, NewTransport(o, base))}, o: o}
}

// ErrTooLarge is an answer longer than the caller of Fetch said it would
// hold.
var ErrTooLarge = errors.New("the answer is too large")

// Fetch sends a request and reads its whole answer, for one small enough to
// hold: at most limit bytes, and a longer one is an error (ErrTooLarge)
// rather than an answer cut to fit. A limit of math.MaxInt64 is no limit.
// The response comes back with its body read and closed, whatever its
// status.
//
// An answer that stops part way - a proxy that drops the connection in the
// middle of a body - is asked for again as any other dropped connection is,
// which the retry inside a transport cannot do: the body is read after the
// transport has returned. A request that is not safe to repeat is not sent
// again for it, and none is sent more than Retry's Tries in all, however it
// failed each time.
func (c *Client) Fetch(req *http.Request, limit int64) (*http.Response, []byte, error) {
	req, again := counted(req)
	tries := c.o.Retry.tries()
	for {
		resp, err := c.Do(req)
		if err != nil {
			return nil, nil, err
		}

		// one byte past the limit is read to know the answer went on; the
		// largest limit there is has no byte past it, and is no limit
		var answer io.Reader = resp.Body
		if limit < math.MaxInt64 {
			answer = io.LimitReader(resp.Body, limit+1)
		}
		body, err := io.ReadAll(answer)
		_ = resp.Body.Close()
		if err == nil {
			if int64(len(body)) > limit {
				return resp, nil, fmt.Errorf("%w: over %s, more than this client reads", ErrTooLarge, size(limit))
			}

			return resp, body, nil
		}

		if *again >= tries-1 || !retrySafe(req) || !c.o.Retry.failed(err) || req.Context().Err() != nil || !rewind(req) {
			return resp, nil, tried(fmt.Errorf("reading the answer: %w", err), *again+1)
		}

		wait := c.o.Retry.wait(*again)
		c.debugf("%s answer stopped part way (try %d of %d), asking again in %s: %v", c.o.Name, *again+1, tries, wait, err)
		*again++
		if !sleep(req.Context(), wait) {
			return resp, nil, errors.Join(req.Context().Err(), err)
		}
	}
}

func (c *Client) debugf(format string, args ...any) {
	if c.o.Log != nil {
		c.o.Log.Debugf(format, args...)
	}
}

// size writes a number of bytes the way a person says it.
func size(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n>>10, 10) + " KiB"
	}

	return strconv.FormatInt(n, 10) + " bytes"
}

// NewBaseTransport returns http.DefaultTransport tuned with per-attempt
// timeouts so a stalled connection or unresponsive server fails fast instead
// of hanging the command: ten seconds to connect, ten for TLS, and the
// options' HeaderWait for the server to start answering. It is exported so
// callers that must build their own client (oauth2, for one) can still start
// from the same transport.
func NewBaseTransport(o Options) http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport // unreachable, but degrade gracefully
	}

	c := t.Clone()
	c.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	c.TLSHandshakeTimeout = 10 * time.Second
	c.ResponseHeaderTimeout = o.HeaderWait
	if o.HeaderWait == 0 {
		c.ResponseHeaderTimeout = DefaultHeaderWait
	}

	return c
}

// Transport is an http.RoundTripper that traces each request and answer to
// the options' Log, with what is secret blanked (see Options) and JSON
// bodies pretty-printed, and that says of a request that got no answer what
// the operating system may be doing about it, where that is known (see
// Explain). With no Log it traces nothing and only explains.
//
// While tracing is on, the start of an answer that is text is read before
// the answer is handed back, up to what the trace prints: an answer that
// trickles in is held until that much has come or it ends.
type Transport struct {
	o         Options
	transport http.RoundTripper
	secrets   secrets
}

// NewTransport wraps next with the trace the options ask for.
func NewTransport(o Options, next http.RoundTripper) *Transport {
	return &Transport{o: o, transport: next, secrets: newSecrets(o)}
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.o.Log != nil {
		t.o.Log.Tracef(logReqMsg, t.o.Name, lazy(func() string { return t.requestText(req) }))
	}

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		return resp, Explain(err)
	}

	if t.o.Log != nil {
		t.o.Log.Tracef(logRespMsg, t.o.Name, lazy(func() string { return t.responseText(resp) }))
	}

	return resp, nil
}

// RetryTransport wraps an http.RoundTripper with the retries the options
// ask for (see Retry): 429 for every request, and a dropped connection or a
// status worth another try for requests that are safe to repeat. A 429's
// Retry-After header is honoured up to MaxRetryAfter, and a cancelled
// request context aborts the wait.
type RetryTransport struct {
	o         Options
	transport http.RoundTripper
}

// NewRetryTransport wraps next with the retries the options ask for.
func NewRetryTransport(o Options, next http.RoundTripper) *RetryTransport {
	return &RetryTransport{o: o, transport: next}
}

// RoundTrip implements http.RoundTripper.
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// the count of times sent again is the request's, not this call's: a
	// request Fetch is asking for again arrives with some of its tries used
	req, again := counted(req)
	safe := retrySafe(req)
	tries := t.o.Retry.tries()
	for {
		resp, err := t.transport.RoundTrip(req)
		left := *again < tries-1
		if err != nil {
			// a transport error can land after the server committed the write
			// (the response just never made it back), so only retry-safe
			// requests go again — re-posting a comment would duplicate it
			if left && safe && t.o.Retry.failed(err) && req.Context().Err() == nil && rewind(req) {
				wait := t.o.Retry.wait(*again)
				t.debugf("%s request failed (try %d of %d), retrying in %s: %v", t.o.Name, *again+1, tries, wait, err)
				*again++
				if !sleep(req.Context(), wait) {
					return nil, errors.Join(req.Context().Err(), err)
				}

				continue
			}

			return nil, tried(err, *again+1)
		}

		// 429 (rate limited) was rejected before it was acted on, so every
		// request may retry it; a server error leaves a mutation's fate
		// unknown, so only retry-safe requests ride through those
		if resp.StatusCode == http.StatusTooManyRequests || (safe && t.o.Retry.status(resp.StatusCode)) {
			if left && rewind(req) {
				wait := t.o.Retry.wait(*again)
				if resp.StatusCode == http.StatusTooManyRequests {
					if ra, ok := retryAfter(resp.Header.Get("Retry-After"), time.Now()); ok {
						wait = ra
					}
				}
				t.debugf("%s got status %d (try %d of %d), retrying in %s", t.o.Name, resp.StatusCode, *again+1, tries, wait)
				*again++
				_ = resp.Body.Close()
				if !sleep(req.Context(), wait) {
					return nil, req.Context().Err()
				}

				continue
			}
		}

		return resp, nil
	}
}

func (t *RetryTransport) debugf(format string, args ...any) {
	if t.o.Log != nil {
		t.o.Log.Debugf(format, args...)
	}
}

// rewind puts a request's body back to its start for another attempt,
// reporting whether it could: a request body is consumed by each attempt,
// and one with no GetBody cannot be sent twice. http.NoBody counts as no
// body — NewRequest leaves GetBody nil for it.
func rewind(req *http.Request) bool {
	if req.Body == nil || req.Body == http.NoBody {
		return true
	}
	if req.GetBody == nil {
		return false
	}
	body, err := req.GetBody()
	if err != nil {
		return false
	}
	req.Body = body

	return true
}

// triedError is a request that got no answer however often it was sent,
// saying how often.
type triedError struct {
	err   error
	tries int
}

func (e *triedError) Error() string { return fmt.Sprintf("%v (tried %d times)", e.err, e.tries) }
func (e *triedError) Unwrap() error { return e.err }

// tried says, of a failure that was tried more than once, how many times.
func tried(err error, tries int) error {
	if tries < 2 {
		return err
	}
	if _, done := errors.AsType[*triedError](err); done {
		return err
	}

	return &triedError{err: err, tries: tries}
}

// backoff is the exponential wait before the attempt after attempt: 1s, 2s, 4s...
func backoff(attempt int) time.Duration {
	return time.Duration(1<<attempt) * time.Second
}

// sleep waits for d unless ctx is done first, reporting whether the full wait
// completed. A cancelled request should not sit out a backoff it will never use.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// retryAfter parses a Retry-After header value, either delay-seconds or an
// HTTP-date, into a wait relative to now. It reports false for an absent or
// unparsable value, and clamps the result to [0, MaxRetryAfter] so a server
// cannot park the tool indefinitely.
func retryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	var wait time.Duration
	if secs, err := strconv.Atoi(value); err == nil {
		wait = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		wait = at.Sub(now)
	} else {
		return 0, false
	}

	return max(0, min(wait, MaxRetryAfter)), true
}
