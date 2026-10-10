//go:build integration

package acceptance

import (
	"strings"
	"testing"

	"github.com/katbyte/go-kt/mcp/acctest"
	"github.com/katbyte/go-kt/version"
)

// server_info names the Sonarr it reached and the build answering, and counts
// the library the seed built.
func TestServerInfo(t *testing.T) {
	out := call(t, "server_info", nil)

	if !strings.HasPrefix(acctest.Str(out["sonarr_version"]), "4.") {
		t.Errorf("sonarr_version = %v, want a Sonarr 4", out["sonarr_version"])
	}
	if out["docker"] != true || acctest.Str(out["operating_system"]) == "" || acctest.Str(out["database"]) == "" {
		t.Errorf("server_info = %v", out)
	}
	if got := acctest.Str(out["sonarr_mcp_version"]); got != version.Version {
		t.Errorf("sonarr_mcp_version = %q, want %q", got, version.Version)
	}
	if n := acctest.Num(t, out["series"], "series"); n < len(importedSeed)+1 {
		t.Errorf("series = %d, want at least the %d seeded", n, len(importedSeed)+1)
	}
}

func TestServerHealth(t *testing.T) {
	out := call(t, "server_health", nil)

	// errors first; the only one a seeded Sonarr has is the RSS check the
	// suite's indexer setup causes (see TestAuditHealth), and it warns that
	// any host may reach it
	checks := acctest.Rows(t, out["checks"], "checks")
	for i, c := range checks {
		if acctest.Str(c["level"]) == "error" && acctest.Str(c["source"]) != "IndexerRssCheck" {
			t.Errorf("a seeded Sonarr fails %v", c)
		}
		if i > 0 && levelRank(acctest.Str(c["level"])) < levelRank(acctest.Str(checks[i-1]["level"])) {
			t.Errorf("not errors first: %v after %v", c["level"], checks[i-1]["level"])
		}
	}
	allowed := findRow(t, checks, "source", "AllowedHostsCheck")
	if acctest.Str(allowed["level"]) != "warning" || !strings.HasPrefix(acctest.Str(allowed["wiki_url"]), "https://") {
		t.Errorf("AllowedHostsCheck = %v", allowed)
	}
}

func levelRank(level string) int {
	return map[string]int{"error": 0, "warning": 1, "notice": 2}[level]
}

func TestTasks(t *testing.T) {
	out := call(t, "task_list", nil)

	tasks := acctest.Rows(t, out["tasks"], "tasks")
	rss := findRow(t, tasks, "command", "RssSync")
	if acctest.NumOr0(rss["interval_minutes"]) == 0 || acctest.Str(rss["next_execution"]) == "" {
		t.Errorf("RssSync = %v", rss)
	}

	// by command name, or by the name task_list shows, spaces and case aside
	ran := 0
	for _, name := range []string{"CheckHealth", "check health"} {
		run := call(t, "task_run", map[string]any{"task": name})
		if acctest.Str(run["status"]) != "completed" || acctest.NumOr0(run["command_id"]) == 0 {
			t.Errorf("task_run %q = %v", name, run)
		}
		ran = acctest.NumOr0(run["command_id"])
	}
	if msg := callErr(t, "task_run", map[string]any{"task": "Defragment"}); !strings.Contains(msg, "RssSync") {
		t.Errorf("an unknown task = %s", msg)
	}

	// what just ran is in the command list
	cmds := acctest.Rows(t, call(t, "command_list", nil)["commands"], "commands")
	if len(cmds) == 0 {
		t.Fatal("command_list is empty after running a task")
	}
	found := false
	for _, c := range cmds {
		if acctest.Str(c["command"]) == "Check Health" && acctest.Str(c["status"]) == "completed" {
			found = true
		}
	}
	if !found {
		t.Errorf("Check Health is not among the commands: %v", cmds)
	}

	// and it is followed by the id the tool that ran it answered, rather than
	// looked for among Sonarr's own housekeeping
	one := call(t, "command_list", map[string]any{"command_id": ran})
	if rows := acctest.Rows(t, one["commands"], "commands"); len(rows) != 1 || acctest.NumOr0(rows[0]["command_id"]) != ran || acctest.Str(rows[0]["status"]) != "completed" {
		t.Errorf("command %d by its id = %v", ran, one)
	}
	if msg := callErr(t, "command_list", map[string]any{"command_id": 987654321}); !strings.Contains(msg, "no command 987654321") {
		t.Errorf("an unknown command = %s", msg)
	}
}

func TestLogList(t *testing.T) {
	out := call(t, "log_list", map[string]any{"level": "info", "limit": 5})

	entries := acctest.Rows(t, out["entries"], "entries")
	if len(entries) == 0 || len(entries) > 5 || acctest.Num(t, out["total"], "total") < len(entries) {
		t.Fatalf("log_list = %v", out)
	}
	for i, e := range entries {
		if acctest.Str(e["time"]) == "" || acctest.Str(e["logger"]) == "" || acctest.Str(e["message"]) == "" {
			t.Errorf("entry %d = %v", i, e)
		}
		if i > 0 && acctest.Str(e["time"]) > acctest.Str(entries[i-1]["time"]) {
			t.Errorf("not newest first at %d", i)
		}
	}
	// the next page starts where this one said. Sonarr goes on logging, so
	// which entries it holds is not held to the first page's
	if total := acctest.Num(t, out["total"], "total"); total > 5 {
		next := call(t, "log_list", map[string]any{"level": "info", "limit": 5, "offset": acctest.Num(t, out["next_offset"], "next_offset")})
		if acctest.Num(t, out["next_offset"], "next_offset") != 5 || acctest.Num(t, next["offset"], "offset") != 5 || len(acctest.Rows(t, next["entries"], "entries")) == 0 {
			t.Errorf("the page after the first = %v", next)
		}
	}
	// the default is warnings and above
	for _, e := range acctest.Rows(t, call(t, "log_list", nil)["entries"], "entries") {
		if l := acctest.Str(e["level"]); l == "info" || l == "debug" || l == "trace" {
			t.Errorf("the default level included %s: %v", l, e)
		}
	}
	if msg := callErr(t, "log_list", map[string]any{"level": "loud"}); !strings.Contains(msg, "level must be one of") {
		t.Errorf("an unknown level = %s", msg)
	}
}
