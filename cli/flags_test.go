package cli

import (
	"slices"
	"testing"

	"github.com/katbyte/sonarr-mcp/tools"
)

// The binary registers core and nothing else unless asked, so a client that
// just points sonarr-mcp at a server does not spend thousands of tokens of
// context on tools it will not call.
func TestDefaultToolsetsIsCore(t *testing.T) {
	t.Parallel()

	var f FlagData
	opts := f.ToolOptions()
	if !slices.Equal(opts.Toolsets, []string{"core"}) {
		t.Errorf("default toolsets = %v, want [core]", opts.Toolsets)
	}

	got, err := tools.Describe(opts)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(got))
	for _, ti := range got {
		names = append(names, ti.Name)
		if ti.Kind != "read" {
			t.Errorf("%s is %s; the default set should never change Sonarr's state", ti.Name, ti.Kind)
		}
	}
	want := slices.Clone(tools.Toolsets["core"])
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("default registered %v, want %v", names, want)
	}
}

// An explicit --toolsets replaces the default rather than adding to it, and
// the other flags reach the options.
func TestExplicitToolsetsOverrideTheDefault(t *testing.T) {
	t.Parallel()

	f := FlagData{Toolsets: []string{"all"}, EnableDelete: true, ReadOnly: true, AllowTools: []string{"a"}, DenyTools: []string{"b"}}
	opts := f.ToolOptions()
	if !opts.EnableDelete || !opts.ReadOnly || len(opts.Allow) != 1 || len(opts.Deny) != 1 {
		t.Errorf("options lost a flag: %+v", opts)
	}
	got, err := tools.Describe(tools.Options{Toolsets: opts.Toolsets})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) <= len(tools.Toolsets["core"]) {
		t.Errorf("--toolsets all registered %d tools, want the whole surface", len(got))
	}
}
