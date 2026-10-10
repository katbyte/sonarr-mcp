// Package tools defines the MCP tools exposed by sonarr-mcp, split by the
// resource they act on (series.go, episodes.go, downloads.go, ...) and the
// audits (audit_*.go). Tools are named resource-first (series_*, episode_*,
// queue_*, audit_*) so they group by what they act on.
//
// Every tool is registered through add with a kind: read tools never change
// Sonarr's state, write tools do (and are dropped under --read-only), and
// delete tools remove series or episode files (and are only registered with
// --enable-delete). --toolsets picks the groups a session needs,
// --allow-tools adds tools to them by name, and --deny-tools takes tools out
// of whatever is left.
package tools

import (
	"context"
	"sync"

	"github.com/katbyte/go-kt/lock"
	mcpregistry "github.com/katbyte/go-kt/mcp/registry"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options controls which tools are registered.
type Options struct {
	// ReadOnly registers only tools that never change Sonarr's state.
	ReadOnly bool
	// EnableDelete registers the tools that delete series and episode
	// files. Off unless the operator opts in.
	EnableDelete bool
	// Toolsets, when set, restricts registration to the named groups (see
	// Toolsets). "core" is always included, so a set can be asked for on its
	// own.
	Toolsets []string
	// Allow asks for tools by name: exact names, prefix/suffix globs
	// (series_*, *_delete) or the "essential" preset. Beside Toolsets it adds
	// to them, these sets and these tools as well; on its own it is only the
	// tools it names, with no core put beside it. It gets nothing past
	// ReadOnly or EnableDelete.
	Allow []string
	// Deny removes matching tools from whatever Toolsets and Allow asked for:
	// it is how a set is narrowed.
	Deny []string
}

// Toolsets group the tools by the job someone is doing, so a client can load a
// working subset instead of all of them. The whole surface is several
// thousand tokens of tool definitions (name, description, input schema)
// before anyone has asked a question; core alone is a fraction of that.
//
// "all" is every tool, which is what the library does when no toolset is asked
// for; the sonarr-mcp binary defaults to core instead (see
// cli.FlagData.ToolOptions).
//
// --toolsets also takes a resource family - series, episode, queue, audit,
// release, ... - which is every tool with that prefix. Those are derived from
// the registered names rather than listed here, so they cannot go stale.
//
// Every tool belongs to exactly one set (TestToolsetsPartition proves it), and
// core is added to whatever else is asked for, because none of the other sets
// can find a series or read its episodes on their own.
var Toolsets = map[string][]string{
	// enough to find things and read them: the base every other set assumes
	"core": {
		"server_info", "series_list", "series_get", "episode_list", "queue_list", "calendar_list",
	},
	// find what is wrong with a library and fix it: every audit, and every
	// tool an audit's worklist is fixed with - searching and grabbing,
	// clearing the queue and the blocklist, importing what Sonarr lost track
	// of, renaming, re-monitoring, and the profile and tag edits
	"curation": {
		"audit_all", "audit_missing_episodes", "audit_cutoff_unmet", "audit_stuck_downloads", "audit_failed_downloads",
		"audit_unmapped_folders", "audit_missing_folders", "audit_untracked_files", "audit_naming", "audit_missing_files", "audit_quality_mismatch",
		"audit_runtime", "audit_language", "audit_monitoring", "audit_profiles", "audit_series_settings", "audit_health",
		"episode_search", "season_search", "series_search", "wanted_search",
		"release_search", "release_grab", "release_parse",
		"queue_remove", "queue_grab", "history_list", "history_mark_failed", "blocklist_list", "blocklist_remove",
		"import_scan", "import_apply", "series_import", "series_rename",
		"file_list", "file_edit", "series_edit", "season_monitor", "episode_monitor", "series_refresh", "series_rescan",
		"profile_list", "tag_list", "tag_create", "tag_delete",
	},
	// grow the library: find a show and add it, and the root folders it goes in
	"library": {
		"series_lookup", "series_add", "rootfolder_list", "rootfolder_add", "rootfolder_remove",
	},
	// running Sonarr rather than using it: health, scheduled tasks and
	// commands, logs, settings, the indexers and download clients, and the
	// tools that remove series and files
	"admin": {
		"server_health", "task_list", "task_run", "command_list", "log_list", "config_get",
		"indexer_list", "indexer_test", "downloadclient_list", "downloadclient_test", "customformat_list",
		"series_delete", "file_delete",
	},
}

// EssentialTools is the curated preset selected by --allow-tools essential:
// enough to see what is in the library and what is coming, and to go and get
// what is missing.
var EssentialTools = []string{
	"series_list",
	"series_get",
	"calendar_list",
	"queue_list",
	"series_search",
}

// The kinds a tool is added with, in go-kt's registry's terms: a read tool
// never changes what Sonarr holds, a write tool does, and a delete tool
// removes what cannot be put back.
const (
	readTool   = mcpregistry.Read
	writeTool  = mcpregistry.Write
	deleteTool = mcpregistry.Delete
)

// registry is this application's tools and what they share. Which of them a
// session gets, and what each tells a client about itself, is go-kt's
// registry's work (tools), so the allow and deny patterns are checked
// against every tool before any is registered.
type registry struct {
	client *sonarr.Client
	opts   Options
	// tools holds every tool queued, made when the first is (see queued)
	tools *mcpregistry.Registry

	// the library's series, read once and shared by every call; see
	// series_index.go
	seriesOnce sync.Once
	series     *seriesCache

	// the locks that make two edits of one series in a turn take turns; see
	// locks.go
	locksOnce sync.Once
	locks     *lock.Set

	// errorLog is where a handler's panic is logged; nil is the process's
	// log
	errorLog func(format string, args ...any)
}

// toolHints are what a tool tells a client about itself beyond its kind, in
// the MCP annotations' terms. A write tool is destructive when it can change
// or take away what was there - a value written over, a file replaced or
// renamed, a download removed, a record dropped - and additive when it only
// ever adds; idempotent when calling it again with the same arguments changes
// nothing more.
//
// Every write tool has an entry, each decided on its own rather than taken
// from the kind: marked all alike, a search (what it grabs can replace the
// file an episode had), task_run (a task can delete files) and series_rescan
// (files gone from disk are forgotten) read as additive to a client. A write
// tool missing from here is marked destructive, and fails
// TestEveryWriteToolIsHinted.
var toolHints = map[string]mcpregistry.Hints{
	// what Sonarr records about a file written over, the same again the
	// second time
	"file_edit": {Idempotent: true},
	// a tag that was not there; a label there already is refused before
	// anything is sent
	"tag_create": {Additive: true, Idempotent: true},
	// a tag taken away; asked again, there is no tag of that label to take
	"tag_delete": {Idempotent: true},
	// a root folder that was not there; nothing on disk is touched
	"rootfolder_add": {Additive: true},
	// a root folder taken out of Sonarr; asked again, there is none at that
	// path
	"rootfolder_remove": {Idempotent: true},
	// a flag written over, the same again the second time
	"episode_monitor": {Idempotent: true},
	"season_monitor":  {Idempotent: true},
	// what a search grabs is imported over the file an episode had when it
	// is an upgrade, and a second search can grab another release
	"episode_search": {},
	"season_search":  {},
	"series_search":  {},
	"wanted_search":  {},
	// a release sent to the download client, imported over the file an
	// episode had; sent again it is another download
	"release_grab": {},
	// the download and its data go from the download client too; asked
	// again, the id is not in the queue and nothing is sent
	"queue_remove": {Idempotent: true},
	// what is grabbed is imported over the file an episode had; asked again,
	// the download is no longer held and nothing is sent
	"queue_grab": {Idempotent: true},
	// the release is blocklisted and, with failed download handling on,
	// another searched for
	"history_mark_failed": {},
	// entries taken off; asked again, there are none of those ids
	"blocklist_remove": {Idempotent: true},
	// a task can empty the recycling bin, clean up records, grab releases or
	// write a backup
	"task_run": {},
	// a file moved into the library, over the one an episode had: Sonarr's
	// "not an upgrade" is overridden
	"import_apply": {},
	// a series that was not there; one in the library already is refused
	// before anything is sent
	"series_add": {Additive: true, Idempotent: true},
	// values written over, the same again the second time; a new path with
	// move_files moves the files
	"series_edit": {Idempotent: true},
	// titles and air dates written over from TheTVDB, episodes it no longer
	// lists dropped, and files gone from disk forgotten
	"series_refresh": {},
	// files gone from disk are forgotten, and their episodes become missing
	"series_rescan": {},
	// files renamed on disk; once they match the format there is nothing
	// left to rename
	"series_rename": {Idempotent: true},
	// series that were not there, each with a folder already on disk as its
	// path; a folder imported is in the library and is passed over
	"series_import": {Additive: true, Idempotent: true},
}

// queued is every tool queued so far, in go-kt's registry, which is made
// when the first tool is.
func (r *registry) queued() *mcpregistry.Registry {
	if r.tools == nil {
		r.tools = mcpregistry.New(mcpregistry.Config{
			Toolsets:  Toolsets,
			Essential: EssentialTools,
			Hints:     toolHints,
			LogError:  r.errorLog,
		})
	}

	return r.tools
}

// add queues a typed tool for registration. go-kt's registry sets the MCP
// annotations from kind and the tool's hints so clients can tell read-only
// from destructive tools without parsing descriptions, turns a panic in the
// handler into an ordinary tool error, and sends empty collections as []
// rather than null: an AI client reading "episodes": null cannot tell "none"
// from "not fetched", and Go leaves un-appended slices nil. What is this
// application's own is done around the handler here.
func add[In, Out any](r *registry, kind mcpregistry.Kind, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcpregistry.Add(r.queued(), kind, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		// anything that changes Sonarr may have added, edited or removed a
		// series, so the index is read again rather than trusted. Done on a
		// failure too, and on a panic: a write that failed part way may still
		// have landed.
		if kind != readTool {
			defer r.seriesCache().invalidate()
		}

		return h(ctx, req, in)
	})
}

// RegisterAll adds every tool permitted by opts to the MCP server and returns
// the names registered. It fails when an allow/deny pattern matches no tool,
// so a typo cannot silently hide one.
func RegisterAll(server *mcp.Server, client *sonarr.Client, opts Options) ([]string, error) {
	r := &registry{client: client, opts: opts}
	queueTools(r)

	return r.queued().Register(server, opts.selection())
}

// selection is the part of the options that chooses tools, as go-kt's
// registry takes it.
func (o *Options) selection() mcpregistry.Selection {
	return mcpregistry.Selection{
		ReadOnly:     o.ReadOnly,
		EnableDelete: o.EnableDelete,
		Toolsets:     o.Toolsets,
		Allow:        o.Allow,
		Deny:         o.Deny,
	}
}

// queueTools queues every tool, before any filtering.
func queueTools(r *registry) {
	registerServerTools(r)
	registerSeriesTools(r)
	registerSeriesWriteTools(r)
	registerEpisodeTools(r)
	registerFileTools(r)
	registerQueueTools(r)
	registerHistoryTools(r)
	registerReleaseTools(r)
	registerImportTools(r)
	registerConfigTools(r)
	registerProviderTools(r)
	registerAuditTools(r)
}

// ToolInfo describes a registered tool without a server to register it on.
type ToolInfo = mcpregistry.Info

// described is every tool queued with no server behind it, for the answers
// that need none: registration never calls the client, only the handlers
// do.
func described(opts Options) (*mcpregistry.Registry, error) {
	client, err := sonarr.New("https://describe.invalid", "describe")
	if err != nil {
		return nil, err
	}
	r := &registry{client: client, opts: opts}
	queueTools(r)

	return r.queued(), nil
}

// Describe lists the tools opts would register, for `sonarr-mcp tools`. It
// needs no connectivity, and makes the same choice RegisterAll does.
func Describe(opts Options) ([]ToolInfo, error) {
	reg, err := described(opts)
	if err != nil {
		return nil, err
	}

	return reg.Describe(opts.selection())
}

// ToolsetNames lists the curated toolsets, for help output.
func ToolsetNames() []string {
	return mcpregistry.New(mcpregistry.Config{Toolsets: Toolsets}).ToolsetNames()
}

// FamilyNames lists the resource prefixes accepted by --toolsets, for help
// output.
func FamilyNames() []string {
	reg, err := described(Options{})
	if err != nil {
		return nil
	}

	return reg.FamilyNames()
}
