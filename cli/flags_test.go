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

// An allow list with no --toolsets is the whole of what loads, chosen from
// every tool, with the default core not put beside it: narrowed by core as
// well, --allow-tools essential loaded four of its five tools in silence.
// Beside --toolsets it adds the tools it names to the sets.
func TestAnAllowListChoosesFromEveryTool(t *testing.T) {
	t.Parallel()

	names := func(f FlagData) []string {
		list, err := tools.Describe(f.ToolOptions())
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(list))
		for _, ti := range list {
			out = append(out, ti.Name)
		}

		return out
	}
	with := func(base []string, more ...string) []string {
		out := slices.Clone(base)
		for _, name := range more {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		slices.Sort(out)

		return out
	}
	essential := []string{"essential"}
	want := slices.Clone(tools.EssentialTools)
	slices.Sort(want)

	if got := names(FlagData{AllowTools: essential}); !slices.Equal(got, want) {
		t.Errorf("--allow-tools essential = %v, want %v", got, want)
	}
	if got := names(FlagData{AllowTools: []string{"tag_*"}}); !slices.Equal(got, []string{"tag_create", "tag_list", "tag_rename"}) {
		t.Errorf("--allow-tools tag_* = %v", got)
	}

	// beside --toolsets it adds to them: the sets, and the tools named as well
	sets := []string{"library"}
	library := names(FlagData{Toolsets: sets})
	if got := names(FlagData{Toolsets: sets, AllowTools: essential}); !slices.Equal(got, with(library, want...)) || !slices.Contains(got, "series_search") {
		t.Errorf("--toolsets library --allow-tools essential = %v, want %v", got, with(library, want...))
	}
	if got := names(FlagData{Toolsets: sets, AllowTools: []string{"task_list"}}); !slices.Equal(got, with(library, "task_list")) {
		t.Errorf("--toolsets library --allow-tools task_list = %v", got)
	}

	// with neither, the default is still core
	if opts := (&FlagData{}).ToolOptions(); !slices.Equal(opts.Toolsets, DefaultToolsets) {
		t.Errorf("no flags = %v, want the default %v", opts.Toolsets, DefaultToolsets)
	}
}

// The tools command lists the sets in an order of its own, and a set left out
// of that order is a set whose tools it does not list.
func TestToolsListsEveryToolset(t *testing.T) {
	t.Parallel()

	for _, set := range tools.ToolsetNames() {
		if !slices.Contains(toolsetOrder, set) {
			t.Errorf("the tools command does not list the %s toolset", set)
		}
	}
}
