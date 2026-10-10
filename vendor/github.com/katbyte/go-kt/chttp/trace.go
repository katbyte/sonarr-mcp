package chttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// DefaultTraceBody is how much of a body a trace prints: enough to see what
// was asked and what came back, and not a library's whole listing.
const DefaultTraceBody = 8 << 10

// redacted stands in a trace for a value that is not shown.
const redacted = "REDACTED"

// secretHeaders are the headers a credential travels in whatever the API.
var secretHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"}

// secretEndings are how the name of a credential ends, lowercased, whatever
// an API puts in front: primaryNodePassword, sharedSecret, refresh_token,
// TmdbApiKey.
// A name is matched by its ending because a name left off a list is a
// credential in a log, with nothing to say so; the price is a page token
// that a trace hides too.
var secretEndings = []string{"password", "secret", "token", "apikey", "api_key"}

// defaultSecrets is what is hidden when nothing more is named.
var defaultSecrets = newSecrets(Options{})

// RedactJSON is JSON text, whole or cut short, with the value of every string
// field that holds a credential blanked: a field of one of the names an API
// passes a credential under, one of also, or one whose name ends as a
// credential's does (password, secret, token, apikey, api_key). It is the
// rule a trace hides a body's credentials by, for whatever else writes down
// what it was sent.
func RedactJSON(text string, also ...string) string {
	s := defaultSecrets
	if len(also) > 0 {
		s = newSecrets(Options{SecretNames: also})
	}

	return s.fields.ReplaceAllString(text, `${1}"`+redacted+`"`)
}

// lazy is text put together only when something formats it, which is how a
// trace costs nothing while tracing is off (see Logger).
type lazy func() string

func (l lazy) String() string { return l() }

// secrets is what a trace must not show.
type secrets struct {
	headers []string
	names   []string
	// fields finds a JSON string field with one of the names, in a body
	// that may have been cut off part way
	fields *regexp.Regexp
}

func newSecrets(o Options) secrets {
	s := secrets{headers: append(slices.Clone(secretHeaders), o.SecretHeaders...)}
	for _, n := range append(slices.Clone(credentialParams), o.SecretNames...) {
		s.names = append(s.names, strings.ToLower(n))
	}

	// a field with one of the names, or with a name that ends as a
	// credential's does
	named := make([]string, 0, len(s.names)+1)
	for _, n := range s.names {
		named = append(named, regexp.QuoteMeta(n))
	}
	named = append(named, `[^"\\]*(?:`+strings.Join(secretEndings, "|")+`)`)
	s.fields = regexp.MustCompile(`(?i)("(?:` + strings.Join(named, "|") + `)"\s*:\s*)"(?:[^"\\]|\\.)*"?`)

	return s
}

func (s secrets) header(name string) bool {
	return slices.ContainsFunc(s.headers, func(h string) bool { return strings.EqualFold(h, name) })
}

// name reports whether a query parameter or a form field of this name holds
// a credential: one of the names, or a name that ends as a credential's
// does.
func (s secrets) name(name string) bool {
	name = strings.ToLower(name)

	return slices.Contains(s.names, name) || slices.ContainsFunc(secretEndings, func(ending string) bool { return strings.HasSuffix(name, ending) })
}

// query is a query string, or a form sent as one, with the value of every
// credential in it blanked and the rest as it was sent.
func (s secrets) query(raw string) string {
	parts := strings.Split(raw, "&")
	for i, p := range parts {
		key, _, hasValue := strings.Cut(p, "=")
		name, err := url.QueryUnescape(key)
		if err != nil {
			name = key
		}
		if hasValue && s.name(name) {
			parts[i] = key + "=" + redacted
		}
	}

	return strings.Join(parts, "&")
}

// requestText is a request as a trace shows it: its line, its headers and
// what can be shown of its body.
func (t *Transport) requestText(req *http.Request) string {
	var b strings.Builder
	path, query, hasQuery := strings.Cut(req.URL.RequestURI(), "?")
	if hasQuery {
		path += "?" + t.secrets.query(query)
	}
	fmt.Fprintf(&b, "%s %s %s\n", req.Method, path, req.Proto)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	fmt.Fprintf(&b, "Host: %s\n", host)
	t.writeHeaders(&b, req.Header)

	switch {
	case req.Body == nil || req.Body == http.NoBody:
	case t.o.TraceBody < 0:
		b.WriteString("\n(a body, not shown)")
	case req.GetBody == nil:
		// reading it here would take it from the server it is being sent to
		b.WriteString("\n(a body sent as a stream, not shown)")
	default:
		body, err := req.GetBody()
		if err != nil {
			fmt.Fprintf(&b, "\n(a body that could not be read again to show: %v)", err)

			break
		}
		head, _ := readAhead(body, t.traceBody()+1)
		_ = body.Close()
		b.WriteString(t.bodyText(head, req.Header.Get("Content-Type")))
	}

	return b.String()
}

// responseText is an answer as a trace shows it. The start of its body is
// read to show it and handed back in front of the rest, so whoever reads the
// answer gets every byte, and what is not shown is never read here: a file
// being downloaded stays a stream.
func (t *Transport) responseText(resp *http.Response) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", resp.Proto, resp.Status)
	t.writeHeaders(&b, resp.Header)

	_, handedOver := resp.Body.(io.Writer)
	switch {
	case resp.Body == nil || resp.Body == http.NoBody:
	case handedOver:
		// a connection switched to another protocol is the caller's to
		// read and write, not an answer to show
	case t.o.TraceBody < 0:
		b.WriteString("\n(a body, not shown)")
	case !textual(resp.Header.Get("Content-Type")):
		fmt.Fprintf(&b, "\n(a body of %s, not shown)", contentOf(resp))
	default:
		head, rest := readAhead(resp.Body, t.traceBody()+1)
		resp.Body = rest
		b.WriteString(t.bodyText(head, resp.Header.Get("Content-Type")))
	}

	return b.String()
}

func (t *Transport) traceBody() int {
	if t.o.TraceBody == 0 {
		return DefaultTraceBody
	}

	return t.o.TraceBody
}

func (t *Transport) writeHeaders(b *strings.Builder, h http.Header) {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		for _, v := range h[name] {
			if t.secrets.header(name) {
				v = redacted
			}
			fmt.Fprintf(b, "%s: %s\n", name, v)
		}
	}
}

// bodyText is the start of a body as a trace shows it: what is secret
// blanked, JSON pretty-printed when all of it is there, and a note when
// there was more than is shown. head holds one byte more than is shown when
// the body went on.
func (t *Transport) bodyText(head []byte, contentType string) string {
	if len(head) == 0 {
		return ""
	}

	limit, more := t.traceBody(), false
	if len(head) > limit {
		head, more = head[:limit], true
		// not half a character: back to where the last one starts, and
		// without it when the cut fell inside it
		last := len(head)
		for last > 0 && !utf8.RuneStart(head[last-1]) {
			last--
		}
		if last > 0 && !utf8.FullRune(head[last-1:]) {
			head = head[:last-1]
		}
	}
	if !utf8.Valid(head) {
		return "\n(a body that is not text, not shown)"
	}

	text := string(head)
	if mediaType(contentType) == "application/x-www-form-urlencoded" {
		text = t.secrets.query(text)
	} else {
		text = t.secrets.fields.ReplaceAllString(text, `${1}"`+redacted+`"`)
	}
	if !more && json.Valid([]byte(text)) {
		var out bytes.Buffer
		//nolint:errcheck,gosec // json.Indent only fails on invalid input, which json.Valid just ruled out
		json.Indent(&out, []byte(text), "", " ")
		text = out.String()
	}
	if more {
		text += fmt.Sprintf("\n... (the first %s; the rest is not shown)", size(int64(len(head))))
	}

	return "\n" + text
}

// textual reports whether a body of this type is worth showing as text. One
// that does not say what it is may be, and is shown if it turns out to be.
func textual(contentType string) bool {
	mt := mediaType(contentType)
	switch {
	case mt == "", strings.HasPrefix(mt, "text/"):
		// a stream of events never ends, and waiting for the start of one
		// to show would hold its reader up
		return mt != "text/event-stream"
	case strings.HasSuffix(mt, "json"), strings.HasSuffix(mt, "xml"), mt == "application/x-www-form-urlencoded", mt == "application/javascript", mt == "application/graphql":
		return true
	}

	return false
}

func mediaType(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(contentType))
	}

	return mt
}

// contentOf says what a body that is not shown is: its type, and its length
// where the server gave one.
func contentOf(resp *http.Response) string {
	what := mediaType(resp.Header.Get("Content-Type"))
	if resp.ContentLength >= 0 {
		what += fmt.Sprintf(", %d bytes", resp.ContentLength)
	}

	return what
}

// readAhead reads up to n bytes from the start of a body and returns them
// with a body that gives every byte the first would have, those included,
// and ends as it would have: the error a read ahead met is met again where
// it was, not swallowed, so an answer cut short still reads as one.
func readAhead(body io.ReadCloser, n int) ([]byte, io.ReadCloser) {
	head := make([]byte, 0, n)
	var err error
	for len(head) < n && err == nil {
		var m int
		m, err = body.Read(head[len(head):n])
		head = head[:len(head)+m]
	}

	return head, &readAheadBody{head: head, err: err, body: body}
}

type readAheadBody struct {
	head []byte
	err  error
	body io.ReadCloser
}

func (r *readAheadBody) Read(p []byte) (int, error) {
	if len(r.head) > 0 {
		n := copy(p, r.head)
		r.head = r.head[n:]

		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}

	return r.body.Read(p)
}

func (r *readAheadBody) Close() error { return r.body.Close() }

const logReqMsg = `%s API Request Details:
---[ REQUEST ]---------------------------------------
%s
-----------------------------------------------------`

const logRespMsg = `%s API Response Details:
---[ RESPONSE ]--------------------------------------
%s
-----------------------------------------------------`
