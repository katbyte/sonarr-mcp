package chttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

// explained is an error with what the operating system may be doing about
// it said after it.
type explained struct {
	err  error
	hint string
}

func (e *explained) Error() string { return e.err.Error() + e.hint }
func (e *explained) Unwrap() error { return e.err }

// Explain adds, to the error of a request that never got an answer, what the
// operating system may be doing about it where that is known: a dial a Mac
// refused with "no route to host" is usually its Local Network privacy,
// which no retry and no server fixes, and the bare error sends its reader
// to the network. The error keeps what it wraps. One there is nothing to
// say about, and one already explained, comes back as it was.
//
// Transport does this for every request it carries; Explain is for a client
// built on another transport.
func Explain(err error) error {
	if localNetworkHint == "" || err == nil || !errors.Is(err, syscall.EHOSTUNREACH) {
		return err
	}
	if _, done := errors.AsType[*explained](err); done {
		return err
	}

	return &explained{err: err, hint: localNetworkHint}
}

// Dropped reports whether a request failed because a connection that was
// there went away: reset or closed by the other end, or ended in the middle
// of an answer, over HTTP/2 as a stream the server or a proxy gave up. It is
// what a client takes as worth another try unless told otherwise (see
// Retry).
//
// A server that could not be reached at all, a name that did not resolve, a
// certificate that did not check out, a wait that ran out and a request that
// was cancelled are not: the next attempt would meet the same.
func Dropped(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return false
	}
	for _, gone := range []error{syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE, io.ErrUnexpectedEOF, io.EOF, net.ErrClosed} {
		if errors.Is(err, gone) {
			return true
		}
	}

	// net/http says some of these in words alone
	msg := err.Error()
	if slices.ContainsFunc([]string{"server closed idle connection", "connection reset by peer", "broken pipe", "unexpected EOF", "server sent GOAWAY", "http2: client connection lost"}, func(words string) bool { return strings.Contains(msg, words) }) {
		return true
	}

	// over HTTP/2 an answer given up part way is a stream reset, which is
	// how one cut short behind a proxy arrives; a stream reset for a broken
	// protocol is not that, and would be again
	return strings.Contains(msg, "stream error: ") && slices.ContainsFunc([]string{"; INTERNAL_ERROR", "; CANCEL", "; REFUSED_STREAM"}, func(code string) bool { return strings.Contains(msg, code) })
}

// Refused reports whether a request failed because nothing was listening
// where it was sent. That is not a dropped connection, and a client does not
// try again for it unless told to: a caller whose server restarts under it
// says so, with func(err error) bool { return Dropped(err) || Refused(err) }
// as its Retry's Error.
func Refused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

// credentialParams are the query parameters a credential commonly travels
// in, lowercased.
var credentialParams = []string{"api_key", "apikey", "api_token", "access_token", "token"}

// RedactURL replaces the value of every credential query parameter in a URL
// with REDACTED, and any password in it, leaving the rest as it was sent:
// the parameters around it, their order and a fragment. The parameters are
// the common ones (api_key, apikey, api_token, access_token, token) and
// whatever also names, compared without case.
func RedactURL(raw string, also ...string) string {
	base, query, found := strings.Cut(raw, "?")
	if u, err := url.Parse(base); err == nil && u.User != nil {
		base = u.Redacted()
	}
	if !found {
		return base
	}
	query, fragment, hasFragment := strings.Cut(query, "#")
	parts := strings.Split(query, "&")
	for i, p := range parts {
		key, _, _ := strings.Cut(p, "=")
		name, err := url.QueryUnescape(key)
		if err != nil {
			name = key
		}
		secret := func(param string) bool { return strings.EqualFold(param, name) }
		if slices.ContainsFunc(credentialParams, secret) || slices.ContainsFunc(also, secret) {
			parts[i] = key + "=REDACTED"
		}
	}
	out := base + "?" + strings.Join(parts, "&")
	if hasFragment {
		out += "#" + fragment
	}

	return out
}

// RedactError hides the credentials in the URL a failed request names. A
// request that gets no answer (a timeout, a refused connection) fails with a
// *url.Error that prints its whole URL, and a key sent in the query is in
// it, so it would reach a log or a tool's answer. The error keeps its type
// and what it wraps; only the URL it prints changes (see RedactURL).
//
// It is for the place the error is first seen: a message already built
// around the error (fmt.Errorf with %w) keeps the text it was built with.
func RedactError(err error, also ...string) error {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		urlErr.URL = RedactURL(urlErr.URL, also...)
	}

	return err
}

// KeepCredentialsOnHost is a redirect policy, for an http.Client's
// CheckRedirect: a redirect is followed (up to Go's usual ten), but one that
// leaves the first request's host, or goes from https down to http, reaches
// its target without the credentials.
//
// Go itself drops only Authorization on the way to another domain, and keeps
// it for a subdomain; a token an API takes in a header of its own it would
// hand to whatever host a redirect names. This drops Authorization and the
// headers named. Another port on the same host is the same machine, and is
// left alone.
func KeepCredentialsOnHost(headers ...string) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		first := via[0].URL
		if strings.EqualFold(req.URL.Hostname(), first.Hostname()) && (first.Scheme != "https" || req.URL.Scheme == "https") {
			return nil
		}
		req.Header.Del("Authorization")
		for _, h := range headers {
			req.Header.Del(h)
		}

		return nil
	}
}

// RedirectError is a request that was redirected and not followed (see
// RefuseRedirects).
type RedirectError struct {
	// Method and Path are the request that was redirected
	Method string
	Path   string
	// To is where it was sent, with the credentials in it hidden
	To string
	// Advice is what the caller knows to do about it on this API: which
	// setting holds the address to put right
	Advice string
}

func (e *RedirectError) Error() string {
	msg := fmt.Sprintf("%s %s was redirected to %s", e.Method, e.Path, e.To)
	if e.Advice != "" {
		msg += ": " + e.Advice
	}

	return msg
}

// RefuseRedirects is a redirect policy, for an http.Client's CheckRedirect:
// no redirect is followed, and the request fails with a *RedirectError that
// says where it was sent and gives the advice.
//
// It is for an API that answers where it is asked, so that a redirect means
// the address points at something in front of it: an http address a proxy
// moves to https, or a login page. Following one is worse than failing. Go
// turns a DELETE, PATCH or POST into a GET on a 301, 302 or 303 and drops
// its body, the GET answers 200, and the write reports success having done
// nothing. An API that does redirect wants KeepCredentialsOnHost.
func RefuseRedirects(advice string) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		return &RedirectError{Method: via[0].Method, Path: via[0].URL.Path, To: RedactURL(req.URL.String()), Advice: advice}
	}
}

// IsWebPage reports whether an answer is an HTML document where an API's own
// answer was expected: a wrong address or a proxy's login page answers 200
// with a page, for a request that never reached the API.
//
// The body decides. A proxy can label anything anything, so a document that
// starts as HTML is a page whatever it is called; and some servers label
// their own one-word replies as HTML, so what is called HTML is a page only
// when it starts with a tag.
func IsWebPage(contentType string, body []byte) bool {
	head := strings.ToLower(strings.TrimSpace(string(body[:min(len(body), 512)])))
	if strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html") {
		return true
	}

	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/html") && strings.HasPrefix(head, "<")
}

// StatusError is an answer with a status the request did not ask for.
type StatusError struct {
	Method string
	Path   string
	// StatusCode is the status the server answered with
	StatusCode int
	// Expected are the statuses the request takes as success, when it
	// names them
	Expected []int
	// Body is the start of the response body (see Preview)
	Body string
	// Tries is how many times the request was sent, when more than once
	Tries int
	// Note is what the caller knows about this status on this API: which
	// key to check for a 401, what permission a 403 wants
	Note string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	if len(e.Expected) > 0 {
		want := make([]string, 0, len(e.Expected))
		for _, c := range e.Expected {
			want = append(want, strconv.Itoa(c))
		}
		msg += " (expected " + strings.Join(want, " or ") + ")"
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	if e.Tries > 1 {
		msg += fmt.Sprintf(" (tried %d times)", e.Tries)
	}
	if e.Note != "" {
		msg += " (" + e.Note + ")"
	}

	return msg
}

// StatusCode is the status of a *StatusError in err's chain, or 0.
func StatusCode(err error) int {
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.StatusCode
	}

	return 0
}

// IsNotFound reports whether err is a 404 from the server.
func IsNotFound(err error) bool { return StatusCode(err) == http.StatusNotFound }

// PreviewLen is how much of a response body a StatusError carries.
const PreviewLen = 300

// Preview is the start of a response body for an error to carry: the space
// around it gone, cut at PreviewLen bytes on a whole character, with "..."
// to say there was more.
func Preview(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) <= PreviewLen {
		return s
	}
	n := PreviewLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}

	return s[:n] + "..."
}
