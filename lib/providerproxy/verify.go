package providerproxy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Drift is one recorded interaction whose live response no longer has the
// shape the cassette captured.
//
// Only the shape is compared, never the values: which film TMDB ranks first
// this week is none of our business, but a field appearing, vanishing or
// changing type is exactly what breaks the server's decoding, and with it
// every identify and refresh the tools depend on.
type Drift struct {
	Key           string
	StatusWas     int
	StatusIs      int
	FieldsAdded   []string
	FieldsRemoved []string
}

func (d Drift) String() string {
	var b strings.Builder
	b.WriteString(d.Key)
	if d.StatusWas != d.StatusIs {
		fmt.Fprintf(&b, "\n    status %d -> %d", d.StatusWas, d.StatusIs)
	}
	if len(d.FieldsRemoved) > 0 {
		fmt.Fprintf(&b, "\n    gone:  %s", strings.Join(d.FieldsRemoved, ", "))
	}
	if len(d.FieldsAdded) > 0 {
		fmt.Fprintf(&b, "\n    new:   %s", strings.Join(d.FieldsAdded, ", "))
	}

	return b.String()
}

// Drifts returns the shape changes seen so far, in a stable order. Empty in
// any mode but Verify.
func (p *Proxy) Drifts() []Drift {
	p.driftMu.Lock()
	defer p.driftMu.Unlock()

	out := append([]Drift(nil), p.drifts...)
	slices.SortFunc(out, func(a, b Drift) int { return strings.Compare(a.Key, b.Key) })

	return out
}

// compare records a drift when live no longer matches the cassette.
func (p *Proxy) compare(recorded, live *interaction) {
	// an elided body (media, images) has no shape to compare
	if recorded.Elided || live.Elided {
		if recorded.Status == live.Status {
			return
		}
	}

	d := Drift{Key: recorded.Key, StatusWas: recorded.Status, StatusIs: live.Status}

	was := fieldPaths(recorded.Body)
	is := fieldPaths(live.Body)
	for _, f := range was {
		if !slices.Contains(is, f) {
			d.FieldsRemoved = append(d.FieldsRemoved, f)
		}
	}
	for _, f := range is {
		if !slices.Contains(was, f) {
			d.FieldsAdded = append(d.FieldsAdded, f)
		}
	}

	if d.StatusWas == d.StatusIs && len(d.FieldsAdded) == 0 && len(d.FieldsRemoved) == 0 {
		return
	}

	p.driftMu.Lock()
	p.drifts = append(p.drifts, d)
	p.driftMu.Unlock()
}

// fieldPaths returns the sorted, deduplicated set of "a.b[].c" paths in a JSON
// document, each with the type of its leaf. Array elements collapse to one
// entry, so a list of ten books and a list of one compare equal.
func fieldPaths(body string) []string {
	if strings.TrimSpace(body) == "" {
		return nil
	}

	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return nil // not JSON: nothing to compare structurally
	}

	seen := map[string]struct{}{}
	walk("", v, seen, 0)

	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.Sort(out)

	return out
}

// maxDepth stops a pathological document from producing an unbounded set.
const maxDepth = 12

func walk(prefix string, v any, seen map[string]struct{}, depth int) {
	if depth > maxDepth {
		return
	}

	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			walk(path, child, seen, depth+1)
		}
	case []any:
		// every element folds into one path, so list length never counts as
		// a change; an empty list still records the path itself
		if len(t) == 0 {
			seen[prefix+"[]"] = struct{}{}
			return
		}
		for _, child := range t {
			walk(prefix+"[]", child, seen, depth+1)
		}
	default:
		seen[prefix+":"+jsonType(v)] = struct{}{}
	}
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	default:
		return "unknown"
	}
}
