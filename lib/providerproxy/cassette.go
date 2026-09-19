package providerproxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// maxBodyBytes caps what a cassette stores. It is generous because a TMDB
// search page or a TheTVDB episode list runs to hundreds of kilobytes and has
// to replay intact or the server will not decode it; what must never be
// committed is media, which elideTypes catches regardless of size.
const maxBodyBytes = 4 << 20

// elideTypes are the content types stored as a placeholder however small they
// are: committing a provider's artwork or video to the repository is never
// right, and neither is what these tests assert on.
var elideTypes = []string{"audio/", "video/", "image/"}

// volatileHeaders change on every response and would make a re-record a large
// meaningless diff. Dropping them is not sanitizing: these are public APIs and
// nothing here is a secret.
var volatileHeaders = map[string]bool{
	"age":                            true,
	"alt-svc":                        true,
	"cf-cache-status":                true,
	"cf-ray":                         true,
	"connection":                     true,
	"content-length":                 true, // recomputed from the body we serve
	"date":                           true,
	"expires":                        true,
	"keep-alive":                     true,
	"nel":                            true,
	"report-to":                      true,
	"reporting-endpoints":            true,
	"server-timing":                  true,
	"set-cookie":                     true,
	"transfer-encoding":              true,
	"x-amz-cf-id":                    true,
	"x-amz-cf-pop":                   true,
	"x-amz-request-id":               true,
	"x-cache":                        true,
	"x-request-id":                   true,
	"x-served-by":                    true,
	"x-timer":                        true,
	"x-cache-hits":                   true,
	"x-memc":                         true,
	"x-memc-age":                     true,
	"x-memc-expires":                 true,
	"x-memc-key":                     true,
	"x-task-id":                      true,
	"etag":                           true,
	"last-modified":                  true,
	"x-apple-jingle-correlation-key": true,
	"apple-seq":                      true,
	"apple-tk":                       true,
	"x-apple-orig-url":               true,
	"x-apple-application-instance":   true,
	"x-apple-application-site":       true,
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

// bytes returns the response body to serve for this interaction. An elided
// media body becomes the smallest valid file of that kind, so the server can
// still decode and store it.
func (i *interaction) bytes() []byte {
	switch {
	case i.Elided:
		switch {
		case strings.Contains(i.ElidedType, "png"):
			return tinyPNG
		case strings.HasPrefix(i.ElidedType, "image/"):
			return tinyJPEG
		default:
			return []byte("elided by the provider proxy")
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

// setBody stores b as text when it is valid UTF-8, and base64 otherwise, so a
// JSON or XML cassette stays readable in a diff. contentType decides whether
// the body is media that must never be committed.
func (i *interaction) setBody(b []byte, contentType string) {
	ct := strings.ToLower(contentType)
	for _, t := range elideTypes {
		if strings.HasPrefix(ct, t) {
			i.Elided, i.ElidedType, i.ElidedSize = true, ct, len(b)
			return
		}
	}
	if len(b) > maxBodyBytes {
		i.Elided, i.ElidedType, i.ElidedSize = true, ct, len(b)
		return
	}
	if utf8.Valid(b) {
		i.Body = string(b)
		return
	}
	i.BodyBase64 = base64.StdEncoding.EncodeToString(b)
}

// cassette is every interaction recorded for one provider host.
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

// key identifies a request by everything that changes the response: method,
// host, path, and the query with its parameters sorted so ordering does not
// produce a spurious miss.
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

// flush writes every changed cassette, sorted by key so a re-record produces a
// minimal diff.
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

// hostFile is the cassette filename for a host, with the characters that are
// awkward in a path replaced.
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

// redactedValue replaces a credential in a recorded body. It is not valid for
// anything, which is the point: a cassette that is replayed never needs one.
const redactedValue = "redacted by the provider proxy"

// redactJSONFields replaces the string value of each named field in a JSON
// body, leaving every other byte where it was so a re-record is a small diff
// and the key order the provider sent is kept. A value carrying an escaped
// quote is matched too. Nothing is parsed: a body that is not JSON has no
// field to match and comes back unchanged.
func redactJSONFields(body string, fields []string) string {
	if body == "" || len(fields) == 0 {
		return body
	}
	for _, f := range fields {
		re := regexp.MustCompile(`("` + regexp.QuoteMeta(f) + `"\s*:\s*)"(?:[^"\\]|\\.)*"`)
		body = re.ReplaceAllString(body, `${1}"`+redactedValue+`"`)
	}

	return body
}
