package tools

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newTestClient(t *testing.T) *sonarr.Client {
	t.Helper()

	c, err := sonarr.New("http://127.0.0.1:1", "test")
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func register(t *testing.T, opts Options) []string {
	t.Helper()

	names, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), opts)
	if err != nil {
		t.Fatal(err)
	}

	return names
}

// kinds maps every tool to its kind, as registration queued it.
func kinds() map[string]toolKind {
	r := &registry{}
	queueTools(r)
	out := map[string]toolKind{}
	for _, p := range r.pending {
		out[p.name] = p.kind
	}

	return out
}

func TestRegisterAllKinds(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	dflt := register(t, Options{})
	ro := register(t, Options{ReadOnly: true})

	if len(all) <= len(dflt) || len(dflt) <= len(ro) || len(ro) == 0 {
		t.Fatalf("counts all=%d default=%d read-only=%d", len(all), len(dflt), len(ro))
	}
	for _, name := range []string{"series_delete", "file_delete"} {
		if slices.Contains(dflt, name) {
			t.Errorf("%s registered without --enable-delete", name)
		}
		if !slices.Contains(all, name) {
			t.Errorf("%s missing with --enable-delete", name)
		}
	}
	// every tool that changes something is named for it, so a name read
	// under --read-only is a read
	for _, name := range ro {
		for _, verb := range []string{"_add", "_edit", "_delete", "_remove", "_create", "_run", "_grab", "_search", "_monitor", "_refresh", "_rescan", "_rename", "_import", "_apply", "_mark_failed"} {
			if strings.HasSuffix(name, verb) && name != "release_search" {
				t.Errorf("%s registered under --read-only", name)
			}
		}
	}
	for _, name := range EssentialTools {
		if !slices.Contains(dflt, name) {
			t.Errorf("essential tool %s does not exist", name)
		}
	}
	if !slices.IsSorted(dflt) {
		t.Error("registered names not sorted")
	}
}

// The delete tools are the ones that remove files or series; nothing else
// is marked destructive, and each of them is.
func TestDeleteToolsAreMarked(t *testing.T) {
	t.Parallel()

	for name, kind := range kinds() {
		isDelete := strings.HasSuffix(name, "_delete") && name != "tag_delete"
		if isDelete != (kind == deleteTool) {
			t.Errorf("%s is kind %d; the tools that delete series or files, and only those, are delete tools", name, kind)
		}
	}
}

func TestRegisterAllFilters(t *testing.T) {
	t.Parallel()

	got := register(t, Options{Allow: []string{"essential"}})
	want := slices.Clone(EssentialTools)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("essential = %v", got)
	}

	got = register(t, Options{Allow: []string{"series_*,queue_list"}, Deny: []string{"*_rescan"}})
	for _, name := range got {
		if !strings.HasPrefix(name, "series_") && name != "queue_list" {
			t.Errorf("unexpected %s", name)
		}
		if name == "series_rescan" {
			t.Error("denied tool registered")
		}
	}
	if !slices.Contains(got, "series_list") || !slices.Contains(got, "queue_list") {
		t.Errorf("allow list not honoured: %v", got)
	}

	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Allow: []string{"bogus_*"}}); err == nil {
		t.Error("unknown allow pattern accepted")
	}
	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Deny: []string{"nope"}}); err == nil {
		t.Error("unknown deny pattern accepted")
	}
}

// Every tool belongs to exactly one toolset, every toolset names only real
// tools, and core comes along with whatever else is asked for.
func TestToolsetsPartition(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	seen := map[string]string{}
	for set, members := range Toolsets {
		for _, m := range members {
			if !slices.Contains(all, m) {
				t.Errorf("toolset %s names %s, which is not a tool", set, m)
			}
			if prev, dup := seen[m]; dup {
				t.Errorf("%s is in both %s and %s", m, prev, set)
			}
			seen[m] = set
		}
	}
	for _, name := range all {
		if seen[name] == "" {
			t.Errorf("%s belongs to no toolset", name)
		}
	}

	got := register(t, Options{Toolsets: []string{"library"}})
	for _, core := range Toolsets["core"] {
		if !slices.Contains(got, core) {
			t.Errorf("core tool %s missing when only library was asked for", core)
		}
	}
	for _, name := range got {
		if seen[name] != "core" && seen[name] != "library" {
			t.Errorf("%s (%s) registered for --toolsets library", name, seen[name])
		}
	}

	// a resource family is every tool with that prefix, plus core
	got = register(t, Options{Toolsets: []string{"queue"}})
	for _, name := range got {
		if !strings.HasPrefix(name, "queue_") && seen[name] != "core" {
			t.Errorf("%s registered for the queue family", name)
		}
	}
	if !slices.Contains(got, "queue_remove") {
		t.Errorf("the queue family lacks queue_remove: %v", got)
	}

	// all is everything the kind gates allow
	if got = register(t, Options{Toolsets: []string{"all"}}); len(got) != len(register(t, Options{})) {
		t.Errorf("all registered %d tools, want %d", len(got), len(register(t, Options{})))
	}

	if _, err := RegisterAll(mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil), newTestClient(t), Options{Toolsets: []string{"nope"}}); err == nil {
		t.Error("unknown toolset accepted")
	} else if !strings.Contains(err.Error(), "curation") || !strings.Contains(err.Error(), "series") {
		t.Errorf("the error should name the sets and families: %v", err)
	}
}

// A session reads the tools it loaded and follows where they point, so a tool
// names only tools that load with it: its own set's, or core's, which comes
// with every set. A core tool loads on its own by default, so it names only
// core tools.
func TestToolsetsNameOnlyWhatTheyLoad(t *testing.T) {
	t.Parallel()

	all := register(t, Options{EnableDelete: true})
	set := map[string]string{}
	for name, members := range Toolsets {
		for _, m := range members {
			set[m] = name
		}
	}
	res, err := session(t, newFakeServer(t), Options{EnableDelete: true}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	word := regexp.MustCompile(`[a-z]+(?:_[a-z]+)+`)
	for _, tool := range res.Tools {
		// the description and the input and output schemas' own descriptions
		raw, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range word.FindAllString(string(raw), -1) {
			if ref == tool.Name || !slices.Contains(all, ref) {
				continue
			}
			if set[ref] != "core" && set[ref] != set[tool.Name] {
				t.Errorf("%s (%s) names %s, which only %s loads", tool.Name, set[tool.Name], ref, set[ref])
			}
		}
	}
}

// Describe reports the same selection RegisterAll makes, with its kinds and
// sets, and needs no server.
func TestDescribe(t *testing.T) {
	t.Parallel()

	list, err := Describe(Options{Toolsets: []string{"curation"}, EnableDelete: true})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	kindOf := map[string]string{}
	for _, ti := range list {
		names = append(names, ti.Name)
		kindOf[ti.Name] = ti.Kind
		if ti.Toolset != "core" && ti.Toolset != "curation" {
			t.Errorf("%s reported in %s", ti.Name, ti.Toolset)
		}
		if ti.Description == "" {
			t.Errorf("%s has no description", ti.Name)
		}
	}
	want := register(t, Options{Toolsets: []string{"curation"}, EnableDelete: true})
	if !slices.Equal(names, want) {
		t.Errorf("Describe = %v\nRegisterAll = %v", names, want)
	}
	if kindOf["audit_all"] != "read" || kindOf["series_edit"] != "write" {
		t.Errorf("kinds = %v", kindOf)
	}
	if _, err := Describe(Options{Allow: []string{"nope"}}); err == nil {
		t.Error("Describe accepted a pattern that matches nothing")
	}

	if fam := FamilyNames(); !slices.Contains(fam, "audit") || !slices.Contains(fam, "series") {
		t.Errorf("families = %v", fam)
	}
	if sets := ToolsetNames(); !slices.Contains(sets, "core") || !slices.IsSorted(sets) {
		t.Errorf("toolset names = %v", sets)
	}
}

// Every tool describes itself in a sentence or more, and every input
// property says what it is for: a model choosing arguments has nothing else
// to go on.
func TestToolsDescribeThemselves(t *testing.T) {
	t.Parallel()

	res, err := session(t, newFakeServer(t), Options{EnableDelete: true}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if len(tool.Description) < 40 || !strings.HasSuffix(strings.TrimSpace(tool.Description), ".") {
			t.Errorf("%s: description %q is not a sentence", tool.Name, tool.Description)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for name, p := range schema.Properties {
			if p.Description == "" && !slices.Contains(selfEvident, name) {
				t.Errorf("%s: input %s has no description", tool.Name, name)
			}
		}
	}
}

// selfEvident are input names that say all there is to say.
var selfEvident = []string{"monitored", "label", "path", "season_folder", "release_group", "quality_profile", "series_type"}

func TestMatchPattern(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"series_get", "series_get", true},
		{"series_get", "series_gets", false},
		{"series_*", "series_get", true},
		{"series_*", "audit_series_settings", false},
		{"*_delete", "file_delete", true},
		{"*", "anything", true},
	} {
		if got := matchPattern(tc.pattern, tc.name); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v", tc.pattern, tc.name, got)
		}
	}
}

// A nil slice in a result must serialise as [], so a client can tell "none"
// from "not fetched".
func TestEmptyNilSlices(t *testing.T) {
	t.Parallel()

	type inner struct{ Tags []string }
	type out struct {
		Items  []inner
		Ptr    *inner
		Names  []string
		Nested [][]string
		Keep   []string
	}
	v := out{Items: []inner{{}}, Ptr: &inner{}, Keep: []string{"x"}}
	emptyNilSlices(reflect.ValueOf(&v).Elem())

	if v.Names == nil || v.Nested == nil || v.Items[0].Tags == nil || v.Ptr.Tags == nil {
		t.Errorf("nil slices survived: %+v", v)
	}
	if len(v.Keep) != 1 {
		t.Error("a populated slice was touched")
	}
}
