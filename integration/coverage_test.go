//go:build integration

package integration

// Operation coverage: the client's transport records every request the suite
// makes, and a whole run checks each of the SDK's operations was made at
// least once. The generated tests prove each method builds its request; only
// a call against Sonarr proves the request is one Sonarr accepts and the
// answer one the method decodes, so an operation nothing calls is an
// operation nothing has proved.

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
)

// notCalled are the operations the suite leaves alone, and why. A name here
// that the suite calls after all fails the run, so the list cannot go stale.
var notCalled = map[string]string{
	"PostSystemRestart":             "restarts Sonarr under the rest of the suite",
	"PostSystemShutdown":            "stops Sonarr, and the container with it",
	"PostSystemBackupRestoreById":   "restores a backup over the running Sonarr, which then restarts",
	"PostSystemBackupRestoreUpload": "the same restore, from an uploaded zip",
}

// recorder is the client's transport: it remembers every request, then hands
// it on.
type recorder struct {
	base http.RoundTripper

	mu   sync.Mutex
	seen []string // "GET /api/v3/series/7"
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.seen = append(r.seen, req.Method+" "+req.URL.Path)
	r.mu.Unlock()

	return r.base.RoundTrip(req)
}

// template is an operation's path as a pattern, and how literal it is: a
// request matches the most literal template it can, so /api/v3/log/file/update
// is GetLogFileUpdate rather than GetLogFileByFilename with "update".
type template struct {
	name     string
	method   string
	path     string
	pattern  *regexp.Regexp
	literals int
}

var placeholder = regexp.MustCompile(`\{[^}]+\}`)

func templates() ([]template, error) {
	svc, err := definitions.Load(filepath.Join("..", "api-definitions", "sonarr"))
	if err != nil {
		return nil, err
	}
	var out []template
	for _, op := range svc.Operations() {
		quoted := regexp.QuoteMeta(op.Path)
		// QuoteMeta escapes the braces too; put the placeholders back as a
		// single path segment each
		quoted = regexp.MustCompile(`\\\{[^}]+\\\}`).ReplaceAllString(quoted, `[^/]+`)
		out = append(out, template{
			name: op.Name, method: op.Method, path: op.Path, pattern: regexp.MustCompile("^" + quoted + "$"),
			literals: len(placeholder.ReplaceAllString(op.Path, "")),
		})
	}

	return out, nil
}

// coverage is what a whole run called: how many of the operations, those no
// request matched that notCalled does not explain, and those notCalled names
// that were called after all.
type coverage struct {
	total, called  int
	missing, stale []string
}

func operationCoverage() (coverage, error) {
	tpls, err := templates()
	if err != nil {
		return coverage{}, err
	}
	called := map[string]bool{}
	rec.mu.Lock()
	seen := slices.Clone(rec.seen)
	rec.mu.Unlock()
	for _, req := range seen {
		method, path, _ := strings.Cut(req, " ")
		best := -1
		for i, tpl := range tpls {
			if tpl.method == method && tpl.pattern.MatchString(path) && (best < 0 || tpl.literals > tpls[best].literals) {
				best = i
			}
		}
		if best >= 0 {
			called[tpls[best].name] = true
		}
	}
	c := coverage{total: len(tpls), called: len(called)}
	for _, tpl := range tpls {
		_, excused := notCalled[tpl.name]
		switch {
		case called[tpl.name] && excused:
			c.stale = append(c.stale, tpl.name)
		case !called[tpl.name] && !excused:
			c.missing = append(c.missing, tpl.name+" ("+tpl.method+" "+tpl.path+")")
		}
	}
	slices.Sort(c.missing)
	slices.Sort(c.stale)

	return c, nil
}

// jsonRaw renders a value as the raw JSON an untyped body takes.
func jsonRaw(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	return b, nil
}
