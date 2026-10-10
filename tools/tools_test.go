package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
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

// kinds maps every tool to its kind - read, write or delete - as Describe
// reports it.
func kinds(t *testing.T) map[string]string {
	t.Helper()

	list, err := Describe(Options{Toolsets: []string{"all"}, EnableDelete: true})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, ti := range list {
		out[ti.Name] = ti.Kind
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

// The surface is 69 tools, 43 of them reads and 2 deletes, and the essential
// preset is enough to see what is in the library and what is coming, and to
// go and get what is missing. A tool added, merged, removed or moved between
// kinds changes these on purpose, and this is where that is said.
func TestSurfaceSize(t *testing.T) {
	t.Parallel()

	count := map[string]int{}
	for _, kind := range kinds(t) {
		count[kind]++
	}
	if total := count["read"] + count["write"] + count["delete"]; total != 141 || count["read"] != 68 || count["write"] != 58 || count["delete"] != 15 {
		t.Errorf("surface = %d tools: %v, want 141 with 68 read, 58 write, 15 delete", total, count)
	}
	for _, want := range []string{"series_list", "series_get", "calendar_list", "queue_list", "series_search"} {
		if !slices.Contains(EssentialTools, want) {
			t.Errorf("essential lacks %s: %v", want, EssentialTools)
		}
	}
	if len(EssentialTools) != 5 {
		t.Errorf("essential = %v, want five tools", EssentialTools)
	}
}

// The delete tools are the ones named for it, each of which removes what
// cannot be put back: nothing else is a delete tool, and each of them is.
func TestDeleteToolsAreMarked(t *testing.T) {
	t.Parallel()

	for name, kind := range kinds(t) {
		if isDelete := strings.HasSuffix(name, "_delete"); isDelete != (kind == deleteTool.String()) {
			t.Errorf("%s is a %s tool; the tools named _delete, and only those, are delete tools", name, kind)
		}
	}
}

// Every tool tells a client what it changes, without the client reading the
// text: a read tool says it changes nothing, every write tool says whether it
// can take away what was there and whether a repeat changes more, from its
// own entry rather than its kind, and every delete is destructive. The ones
// that would read as additive if all writes were marked alike are named.
func TestEveryWriteToolIsHinted(t *testing.T) {
	t.Parallel()

	listed, err := session(t, newFakeServer(t), Options{EnableDelete: true}).ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	kindOf := kinds(t)
	seen := map[string]bool{}
	for _, tool := range listed.Tools {
		a := tool.Annotations
		if a == nil || a.DestructiveHint == nil || a.OpenWorldHint == nil {
			t.Errorf("%s has no annotations", tool.Name)
			continue
		}
		// only a notification sends anything to a person or a service beyond
		// Sonarr: a search or a grab is Sonarr asking its own indexers and
		// download client
		if *a.OpenWorldHint != strings.HasPrefix(tool.Name, "notification_") || (*a.OpenWorldHint && !toolHints[tool.Name].SendsOut) {
			if sends := slices.Contains([]string{"notification_add", "notification_edit", "notification_test"}, tool.Name); *a.OpenWorldHint != sends {
				t.Errorf("%s says it reaches past Sonarr: %v, want %v", tool.Name, *a.OpenWorldHint, sends)
			}
		}
		switch kindOf[tool.Name] {
		case "read":
			if !a.ReadOnlyHint || *a.DestructiveHint {
				t.Errorf("%s: read-only %v, destructive %v", tool.Name, a.ReadOnlyHint, *a.DestructiveHint)
			}
		case "write":
			seen[tool.Name] = true
			h, ok := toolHints[tool.Name]
			if !ok {
				t.Errorf("%s is a write tool with no hints of its own", tool.Name)
				continue
			}
			if a.ReadOnlyHint || *a.DestructiveHint == h.Additive || a.IdempotentHint != h.Idempotent || h.WritesHere {
				t.Errorf("%s: read-only %v, destructive %v, idempotent %v; want %+v", tool.Name, a.ReadOnlyHint, *a.DestructiveHint, a.IdempotentHint, h)
			}
		case "delete":
			if a.ReadOnlyHint || !*a.DestructiveHint {
				t.Errorf("%s deletes: read-only %v, destructive %v", tool.Name, a.ReadOnlyHint, *a.DestructiveHint)
			}
		default:
			t.Errorf("%s has no kind", tool.Name)
		}
	}
	for name := range toolHints {
		// a read tool has hints only to say it sends something out
		if !seen[name] && name != "notification_test" {
			t.Errorf("toolHints names %s, which is no write tool", name)
		}
	}
	for _, name := range []string{"episode_search", "season_search", "series_search", "wanted_search", "release_grab", "import_apply", "task_run", "series_refresh", "series_rescan", "series_rename", "queue_remove", "rootfolder_remove", "backup_restore", "server_restart", "server_shutdown", "config_edit", "profile_edit", "series_bulk_edit"} {
		if h, ok := toolHints[name]; !ok || h.Additive {
			t.Errorf("%s can remove or overwrite what was there, and is marked additive", name)
		}
	}
	for _, name := range []string{"tag_create", "rootfolder_add", "series_add", "series_import"} {
		if !toolHints[name].Additive {
			t.Errorf("%s only ever adds, and no longer says so", name)
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

// Beside toolsets an allow list adds the tools it names to them, these sets
// and these tools as well, whatever set holds them; on its own it is only
// the tools it names, with no core put beside it. It gets nothing past the
// read-only switch or the delete gate, and a set is narrowed with a deny
// list.
func TestAnAllowListBesideToolsets(t *testing.T) {
	t.Parallel()

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
	core := register(t, Options{Toolsets: []string{"core"}})

	// core and the essential five, one of which no part of core is
	if got, want := register(t, Options{Toolsets: []string{"core"}, Allow: []string{"essential"}}), with(core, EssentialTools...); !slices.Equal(got, want) || !slices.Contains(got, "series_search") {
		t.Errorf("essential beside core = %v, want %v", got, want)
	}
	// a pattern adds every tool it names, and nothing else comes with them
	if got, want := register(t, Options{Toolsets: []string{"core"}, Allow: []string{"tag_*"}}), with(core, "tag_create", "tag_list", "tag_rename"); !slices.Equal(got, want) {
		t.Errorf("tag_* beside core = %v, want %v", got, want)
	}
	// a tool of another set's, by name
	if got, want := register(t, Options{Toolsets: []string{"library"}, Allow: []string{"task_list"}}), with(register(t, Options{Toolsets: []string{"library"}}), "task_list"); !slices.Equal(got, want) {
		t.Errorf("task_list beside library = %v, want %v", got, want)
	}

	// naming a tool does not get it past the delete gate or the read-only switch
	if got := register(t, Options{Toolsets: []string{"core"}, Allow: []string{"series_delete"}}); !slices.Equal(got, core) {
		t.Errorf("series_delete beside core, deletes not enabled = %v, want core alone", got)
	}
	if got := register(t, Options{Toolsets: []string{"core"}, Allow: []string{"series_delete"}, EnableDelete: true}); !slices.Equal(got, with(core, "series_delete")) {
		t.Errorf("series_delete beside core, deletes enabled = %v", got)
	}
	if got := register(t, Options{Toolsets: []string{"core"}, Allow: []string{"series_rescan"}, ReadOnly: true}); !slices.Equal(got, core) {
		t.Errorf("series_rescan beside core, read only = %v, want core alone", got)
	}

	// on its own an allow list is the whole of what loads
	if got := register(t, Options{Allow: []string{"task_list"}}); !slices.Equal(got, []string{"task_list"}) {
		t.Errorf("task_list alone = %v", got)
	}
	// and a set is narrowed by denying, which takes from whatever asked
	narrowed := register(t, Options{Toolsets: []string{"library"}, Allow: []string{"task_list"}, Deny: []string{"rootfolder_*", "task_*"}})
	if slices.ContainsFunc(narrowed, func(n string) bool { return strings.HasPrefix(n, "rootfolder_") || strings.HasPrefix(n, "task_") }) || !slices.Contains(narrowed, "series_add") {
		t.Errorf("library and task_list, rootfolder_* and task_* denied = %v", narrowed)
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

// A handler that panics answers its call with an error naming the tool and
// logs the stack, rather than ending the session: the next call is served.
// A write tool that panicked still drops the series index, since it may have
// changed Sonarr before it failed.
func TestAPanicInAToolIsAnErrorNotACrash(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		logged strings.Builder
	)
	r := &registry{client: newTestClient(t), errorLog: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintf(&logged, format, args...)
	}}
	type none struct{}
	add(r, writeTool, &mcp.Tool{Name: "zzyzx_panic"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		var found map[string]*sonarr.SeriesResource

		return nil, none{}, errors.New(found["9"].Title) // a nil dereference
	})
	add(r, readTool, &mcp.Tool{Name: "zzyzx_fine"}, func(context.Context, *mcp.CallToolRequest, none) (*mcp.CallToolResult, none, error) {
		return nil, none{}, nil
	})
	cs := hostRegistry(t, r)
	cache := r.seriesCache()
	cache.idx = &seriesIndex{}

	msg := mustFail(t, cs, "zzyzx_panic", map[string]any{})
	if !strings.Contains(msg, "internal error in zzyzx_panic") || !strings.Contains(msg, "nil pointer") {
		t.Errorf("the refusal = %q, want the tool and the panic named", msg)
	}
	mu.Lock()
	log := logged.String()
	mu.Unlock()
	if !strings.Contains(log, "internal error in zzyzx_panic") || !strings.Contains(log, "goroutine") {
		t.Errorf("the log = %q, want the panic and its stack", log)
	}
	cache.mu.Lock()
	left := cache.idx
	cache.mu.Unlock()
	if left != nil {
		t.Error("a write tool that panicked left the series index standing")
	}
	mustCall(t, cs, "zzyzx_fine", map[string]any{})
}

// notWrapped are the SDK operations no tool calls, each with why: a read a
// list tool covers, a bulk form of what a tool does one at a time, the web
// interface's own plumbing. Every other operation is called by some tool,
// and one here that a tool comes to call is reported, so the list cannot go
// stale. docs/ROADMAP.md says the same in words.
var notWrapped = map[string]string{
	// by-id reads: the list tool reads them all and finds the one asked for
	"GetAutoTaggingById": "autotag_list", "GetCalendarById": "calendar_list", "GetCustomFilterById": "filter_list", "GetCustomFormatById": "customformat_list",
	"GetDelayProfileById": "delayprofile_list", "GetDownloadClientById": "downloadclient_list", "GetEpisodeById": "episode_list", "GetImportListById": "importlist_list",
	"GetImportListExclusionById": "exclusion_list", "GetIndexerById": "indexer_list", "GetLanguageById": "language_list", "GetMetadataById": "metadata_list",
	"GetNotificationById": "notification_list", "GetQualityDefinitionById": "qualitydefinition_list", "GetQualityProfileById": "profile_list", "GetReleaseProfileById": "releaseprofile_list",
	"GetRemotePathMappingById": "remotepathmapping_list", "GetRootFolderById": "rootfolder_list", "GetSystemTaskById": "task_list", "GetTagById": "tag_list", "GetTagDetailById": "tag_list",
	"GetConfigDownloadClientById": "config_get", "GetConfigHostById": "config_get", "GetConfigImportListById": "config_get", "GetConfigIndexerById": "config_get",
	"GetConfigMediaManagementById": "config_get", "GetConfigNamingById": "config_get", "GetConfigUiById": "config_get",
	"GetWantedCutoff": "audit_cutoff_unmet reads the episodes and files themselves", "GetWantedCutoffById": "audit_cutoff_unmet", "GetWantedMissing": "audit_missing_episodes", "GetWantedMissingById": "audit_missing_episodes",
	"GetImportListExclusion": "exclusion_list reads the paged form", "GetImportListExclusionPaged": "exclusion_list reads the paged form by another name",
	"GetHistoryComplete": "history_list pages", "GetHistorySeries": "history_list takes a series", "GetLogComplete": "log_list pages", "GetQueue": "queue_list reads the paged form with details", "GetQueueStatus": "queue_list counts",
	"GetBlocklist": "blocklist_list reads the paged form by another name", "GetFileSystemType": "filesystem_list says what each entry is",
	// what the server answers about itself that a tool has no use for
	"GetApi": "server_info reads the status", "GetPing": "server_info reads the status", "GetIndexerFlag": "a fixed list of flag names a release carries", "GetQualityDefinitionLimits": "the bounds a size may be set within; Sonarr refuses one outside them",
	"GetLocalization": "the web interface's translations", "GetLocalizationById": "the web interface's translations", "GetLocalizationLanguage": "the web interface's translations",
	"GetSystemRoutes": "the routing graph, for Sonarr's own developers", "GetSystemRoutesDuplicate": "the routing graph, for Sonarr's own developers",
	"GetFeedV3CalendarSonarrIcs":        "a link for a calendar application, carrying the API key; calendar_list answers the same episodes",
	"GetMediaCoverBySeriesIdByFilename": "an image; series_get gives the address", "GetLogFileUpdate": "a container is updated by replacing its image, so there is no update log", "GetLogFileUpdateByFilename": "no update log in a container",
	// bulk forms of what a tool does one at a time, each answering for itself
	"DeleteBlocklistBulk": "blocklist_remove", "DeleteCustomFormatBulk": "customformat_delete", "DeleteDownloadClientBulk": "downloadclient_delete", "DeleteEpisodeFileBulk": "file_delete",
	"DeleteImportListBulk": "importlist_delete", "DeleteImportListExclusionBulk": "exclusion_remove", "DeleteIndexerBulk": "indexer_delete", "DeleteQueueBulk": "queue_remove",
	"DeleteSeriesEditor":  "series_delete, one at a time: a delete of many series in one call is the mistake hardest to take back",
	"PutCustomFormatBulk": "customformat_edit", "PutDownloadClientBulk": "downloadclient_edit", "PutImportListBulk": "importlist_edit", "PutIndexerBulk": "indexer_edit",
	"PutQualityDefinitionUpdate": "qualitydefinition_edit, one at a time", "PutEpisodeFileEditor": "file_edit sends each file", "PutEpisodeFileById": "file_edit uses the bulk form, which takes one as well",
	"PutEpisodeById": "episode_monitor uses the monitor form", "PostQueueGrabById": "queue_grab uses the bulk form, which takes one as well", "PutImportListExclusionById": "exclusion_remove and exclusion_add; an exclusion has nothing else to change",
	// the settings pages' own helpers, which answer a form
	"PostDownloadClientActionByName": "a device list or an OAuth start for a form being filled in", "PostImportListActionByName": "a form's helper", "PostIndexerActionByName": "a form's helper",
	"PostMetadataActionByName": "a form's helper", "PostNotificationActionByName": "a form's helper", "PutDelayProfileReorderById": "delayprofile_edit sets order",
	// metadata consumers are a fixed few, turned on and set, never added, deleted or tested
	"PostMetadata": "metadata_edit", "DeleteMetadataById": "metadata_edit turns one off", "GetMetadataSchema": "metadata_list shows what each takes", "PostMetadataTest": "writing files beside the library is not tested", "PostMetadataTestAll": "not tested",
	// kept by Sonarr 4 for old clients and no longer used
	"GetLanguageProfile": "language profiles are gone from Sonarr 4", "GetLanguageProfileById": "gone", "GetLanguageProfileSchema": "gone", "PostLanguageProfile": "gone", "PutLanguageProfileById": "gone", "DeleteLanguageProfileById": "gone",
	// for other programs, not a caller
	"PostReleasePush": "an indexer pushes a release to Sonarr; a caller grabs one with release_grab", "PostManualImport": "import_apply sends the files to the import command, which reprocesses them itself",
	"PostSystemBackupRestoreUpload": "backup_restore restores one Sonarr holds; a model has no file to hand over",
}

// Every operation the Go client has is called by a tool, or is in
// notWrapped with the reason; and nothing in notWrapped is called by a tool
// after all.
func TestEveryOperationHasAToolOrAReason(t *testing.T) {
	t.Parallel()

	var source strings.Builder
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		source.Write(raw)
	}

	client := reflect.TypeFor[sonarr.Client]()
	for method := range client.Methods() {
		name := method.Name
		if !strings.HasPrefix(name, "Get") && !strings.HasPrefix(name, "Post") && !strings.HasPrefix(name, "Put") && !strings.HasPrefix(name, "Delete") {
			continue
		}
		called := strings.Contains(source.String(), "."+name+"(")
		_, excused := notWrapped[name]
		switch {
		case !called && !excused:
			t.Errorf("%s: no tool calls it and notWrapped gives no reason; wrap it, or say why not", name)
		case called && excused:
			t.Errorf("%s: a tool calls it, so take it out of notWrapped", name)
		}
	}
}
