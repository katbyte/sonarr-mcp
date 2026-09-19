package providerproxy

import (
	"slices"
	"testing"
)

// fieldPaths is what decides whether a provider has changed shape, so the
// distinction it draws - structure counts, values and list lengths do not -
// is worth pinning down directly.
const bodyAX = `{"a":"x"}`

func TestFieldPathsIgnoresValuesAndListLength(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, a, b string }{
		{"different values", `{"title":"Dune"}`, `{"title":"Foundation"}`},
		{"different list length", `{"books":[{"id":"1"}]}`, `{"books":[{"id":"1"},{"id":"2"}]}`},
		{"different key order", `{"a":"x","b":1}`, `{"b":1,"a":"x"}`},
		{"different numbers", `{"n":1}`, `{"n":9999.5}`},
	} {
		if a, b := fieldPaths(tc.a), fieldPaths(tc.b); !slices.Equal(a, b) {
			t.Errorf("%s: shapes differ but should not\n  %v\n  %v", tc.name, a, b)
		}
	}
}

func TestFieldPathsCatchesRealChanges(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, a, b string }{
		{"field renamed", `{"book":[]}`, `{"books":[]}`},
		{"field removed", `{"a":"x","b":"y"}`, bodyAX},
		{"type changed", `{"n":1}`, `{"n":"1"}`},
		{"nesting added", `{"providers":["a"]}`, `{"providers":{"books":["a"]}}`},
		{"array of strings became array of objects", `{"p":["a"]}`, `{"p":[{"value":"a"}]}`},
	} {
		if a, b := fieldPaths(tc.a), fieldPaths(tc.b); slices.Equal(a, b) {
			t.Errorf("%s: shapes match but should not: %v", tc.name, a)
		}
	}
}

// the two real client bugs this session, as shape diffs
func TestCompareReportsTheBugsWeShipped(t *testing.T) {
	t.Parallel()

	p := &Proxy{}
	p.compare(
		&interaction{Key: "GET|api.example|/api/me/bookmarks", Status: 200, Body: `[{"title":"x","time":1}]`},
		&interaction{Key: "GET|api.example|/api/me/bookmarks", Status: 200, Body: `{"bookmarks":[{"title":"x","time":1}]}`},
	)

	drifts := p.Drifts()
	if len(drifts) != 1 {
		t.Fatalf("drifts = %d, want 1", len(drifts))
	}
	if len(drifts[0].FieldsAdded) == 0 || len(drifts[0].FieldsRemoved) == 0 {
		t.Errorf("the bookmarks wrapper should show as both added and removed paths: %+v", drifts[0])
	}
}

func TestCompareIgnoresIdenticalShape(t *testing.T) {
	t.Parallel()

	p := &Proxy{}
	p.compare(
		&interaction{Key: "k", Status: 200, Body: `{"results":[{"id":"1","n":1}]}`},
		&interaction{Key: "k", Status: 200, Body: `{"results":[{"id":"2","n":7},{"id":"3","n":9}]}`},
	)
	if d := p.Drifts(); len(d) != 0 {
		t.Errorf("identical shapes reported drift: %+v", d)
	}
}

func TestCompareCatchesStatusChange(t *testing.T) {
	t.Parallel()

	p := &Proxy{}
	p.compare(
		&interaction{Key: "k", Status: 200, Body: bodyAX},
		&interaction{Key: "k", Status: 429, Body: bodyAX},
	)
	drifts := p.Drifts()
	if len(drifts) != 1 || drifts[0].StatusIs != 429 {
		t.Errorf("a status change was not reported: %+v", drifts)
	}
}

func TestFieldPathsNonJSON(t *testing.T) {
	t.Parallel()

	if got := fieldPaths("<rss><channel/></rss>"); got != nil {
		t.Errorf("non-JSON should compare as nothing, got %v", got)
	}
	if got := fieldPaths(""); got != nil {
		t.Errorf("empty body = %v, want nil", got)
	}
}
