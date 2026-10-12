package replayproxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// maxBodyBytes caps what a cassette stores: generous, since a feed runs to hundreds of kilobytes and must replay intact. Media is caught by type.
const maxBodyBytes = 4 << 20

// elideTypes are the content types stored as a placeholder however small: a service's artwork or audio never belongs in the repository. A binary blob
// under application/octet-stream is elided the same way, told by its NUL bytes; text under that type is kept.
var elideTypes = []string{"audio/", "video/", "image/", "application/x-msdownload"}

// volatileHeaders change on every answer and would make a re-record a meaningless diff; a CDN's request ids can also say where the recording machine
// was.
var volatileHeaders = map[string]bool{
	"age":                            true,
	"akamai-cache-status":            true,
	"akamai-grn":                     true,
	"akamai-request-bc":              true,
	"alt-svc":                        true,
	"apple-seq":                      true,
	"apple-tk":                       true,
	"b3":                             true,
	"cf-cache-status":                true,
	"cf-ray":                         true,
	"connection":                     true,
	"content-length":                 true, // recomputed from the body we serve
	"date":                           true,
	"etag":                           true,
	"expires":                        true,
	"keep-alive":                     true,
	"last-modified":                  true,
	"nel":                            true,
	"report-to":                      true,
	"reporting-endpoints":            true,
	"server-timing":                  true,
	"set-cookie":                     true,
	"transfer-encoding":              true,
	"via":                            true,
	"x-amz-cf-id":                    true,
	"x-amz-cf-pop":                   true,
	"x-amz-date":                     true,
	"x-amz-id-2":                     true,
	"x-amz-ir-id":                    true,
	"x-amz-request-id":               true,
	"x-amz-rid":                      true,
	"x-amz-version-id":               true,
	"x-amzn-requestid":               true,
	"x-apple-application-instance":   true,
	"x-apple-application-site":       true,
	"x-apple-jingle-correlation-key": true,
	"x-apple-orig-url":               true,
	"x-apple-request-uuid":           true,
	"x-b3-spanid":                    true,
	"x-b3-traceid":                   true,
	"x-cache":                        true,
	"x-cache-hits":                   true,
	"x-daiquiri-debug-worker-pid":    true,
	"x-daiquiri-instance":            true,
	"x-memc":                         true,
	"x-memc-age":                     true,
	"x-memc-expires":                 true,
	"x-memc-key":                     true,
	"x-ratelimit-remaining":          true,
	"x-ratelimit-reset":              true,
	"x-request-id":                   true,
	"x-responding-instance":          true,
	"x-served-by":                    true,
	"x-task-id":                      true,
	"x-timer":                        true,
	"x-webobjects-loadaverage":       true,
}

// interaction is one recorded request/response pair.
type interaction struct {
	Key     string            `json:"key"`
	Method  string            `json:"method"`
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	Query   string            `json:"query,omitempty"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	// exactly one of Body or BodyBase64 is set, unless the body was elided
	Body       string `json:"body,omitempty"`
	BodyBase64 string `json:"body_base64,omitempty"`
	Elided     bool   `json:"elided,omitempty"`
	ElidedType string `json:"elided_type,omitempty"`
	ElidedSize int    `json:"elided_size,omitempty"`
}

// bytes is the body to serve; an elided media body becomes the smallest valid file of its kind, so the server can still decode it.
func (i *interaction) bytes() []byte {
	switch {
	case i.Elided:
		switch {
		case strings.Contains(i.ElidedType, "png"):
			return tinyPNG
		case strings.HasPrefix(i.ElidedType, "image/"):
			return tinyJPEG
		default:
			return []byte(elidedBody)
		}
	case i.BodyBase64 != "":
		b, err := base64.StdEncoding.DecodeString(i.BodyBase64)
		if err != nil {
			return nil
		}
		return b
	default:
		return []byte(i.Body)
	}
}

// setBody stores b as text when it is UTF-8, base64 otherwise, and elides media by its type.
func (i *interaction) setBody(b []byte, contentType string) {
	ct := strings.ToLower(contentType)
	for _, t := range elideTypes {
		if strings.HasPrefix(ct, t) {
			i.Elided, i.ElidedType, i.ElidedSize = true, ct, len(b)
			return
		}
	}
	if len(b) > maxBodyBytes || (strings.HasPrefix(ct, "application/octet-stream") && bytes.IndexByte(b, 0) >= 0) {
		i.Elided, i.ElidedType, i.ElidedSize = true, ct, len(b)
		return
	}
	if utf8.Valid(b) {
		i.Body = string(b)
		return
	}
	i.BodyBase64 = base64.StdEncoding.EncodeToString(b)
}

// clone copies an interaction, so one copy can be redacted and the other not.
func (i *interaction) clone() *interaction {
	c := *i
	c.Headers = maps.Clone(i.Headers)

	return &c
}

// redact blanks each named JSON field in the body, and the same value in any header, where one service repeats a token.
func (i *interaction) redact(fields []string) {
	var secrets []string
	i.Body, secrets = redactJSONFields(i.Body, fields)
	for name, v := range i.Headers {
		for _, secret := range secrets {
			if strings.Contains(v, secret) {
				i.Headers[name] = strings.ReplaceAll(v, secret, redactedValue)
			}
		}
	}
}

// redacted reports whether a credential was blanked out of the recording.
func (i *interaction) redacted() bool {
	return strings.Contains(i.Body, redactedValue)
}

// cassette is every interaction recorded for one host.
type cassette struct {
	Host         string         `json:"host"`
	Interactions []*interaction `json:"interactions"`
}

// store is the on-disk set of cassettes, one file per host.
type store struct {
	dir string

	mu     sync.Mutex
	byHost map[string]*cassette
	index  map[string]*interaction // key -> interaction, across all hosts
	dirty  map[string]bool
}

func newStore(dir string) (*store, error) {
	s := &store{
		dir:    dir,
		byHost: map[string]*cassette{},
		index:  map[string]*interaction{},
		dirty:  map[string]bool{},
	}

	if dir == "" {
		return s, nil // a proxy that keeps no recordings
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil // nothing recorded yet
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // the cassette directory is ours, not caller input
		if err != nil {
			return nil, err
		}
		var c cassette
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		s.byHost[c.Host] = &c
		for _, i := range c.Interactions {
			s.index[i.Key] = i
		}
	}

	return s, nil
}

// key identifies a request by all that changes its answer: method, host, path and the query, sorted.
func key(method, host, path string, query url.Values) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(method))
	b.WriteString(" ")
	b.WriteString(strings.ToLower(host))
	b.WriteString(path)
	if len(query) > 0 {
		b.WriteString("?")
		b.WriteString(query.Encode()) // Encode sorts by key
	}

	return b.String()
}

func (s *store) lookup(k string) (*interaction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	i, ok := s.index[k]

	return i, ok
}

func (s *store) put(host string, i *interaction) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.byHost[host]
	if !ok {
		c = &cassette{Host: host}
		s.byHost[host] = c
	}
	// a re-record replaces the previous response for the same request
	if prev, exists := s.index[i.Key]; exists {
		for n, existing := range c.Interactions {
			if existing == prev {
				c.Interactions[n] = i
				s.index[i.Key] = i
				s.dirty[host] = true
				return
			}
		}
	}
	c.Interactions = append(c.Interactions, i)
	s.index[i.Key] = i
	s.dirty[host] = true
}

// flush writes every changed cassette, sorted by key for a small diff.
func (s *store) flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.dirty) == 0 {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return err
	}
	for host := range s.dirty {
		c := s.byHost[host]
		slices.SortFunc(c.Interactions, func(a, b *interaction) int {
			return strings.Compare(a.Key, b.Key)
		})
		raw, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		if err := os.WriteFile(filepath.Join(s.dir, hostFile(host)), raw, 0o600); err != nil {
			return err
		}
	}
	s.dirty = map[string]bool{}

	return nil
}

// hostFile is a host's cassette file name.
func hostFile(host string) string {
	return strings.NewReplacer(":", "_", "/", "_").Replace(host) + ".json"
}

// keepHeaders strips the headers that change on every response.
func keepHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for name, vals := range h {
		if volatileHeaders[strings.ToLower(name)] || len(vals) == 0 {
			continue
		}
		out[name] = vals[0]
	}

	return out
}

// redactedValue replaces a credential in a recorded body; the wording is what older cassettes already hold, and is how a recording is known to be
// redacted.
const redactedValue = "redacted by the provider proxy"

// elidedBody is served for an elided body that is no image.
const elidedBody = "elided by the provider proxy"

// redactJSONFields blanks each named string field in a JSON body, leaving every other byte in place so a re-record is a small diff; nothing is
// parsed, so a body that is not JSON comes back as it was.
func redactJSONFields(body string, fields []string) (redacted string, secrets []string) {
	if body == "" || len(fields) == 0 {
		return body, nil
	}
	for _, f := range fields {
		re := regexp.MustCompile(`("` + regexp.QuoteMeta(f) + `"\s*:\s*)"((?:[^"\\]|\\.)*)"`)
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			if m[2] != "" && m[2] != redactedValue {
				secrets = append(secrets, m[2])
			}
		}
		body = re.ReplaceAllString(body, `${1}"`+redactedValue+`"`)
	}

	return body, secrets
}
