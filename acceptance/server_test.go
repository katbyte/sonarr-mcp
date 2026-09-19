//go:build integration

package acceptance

import (
	"strings"
	"testing"

	"github.com/katbyte/go-kt/version"
)

// server_info names the Sonarr it reached and the build answering, and counts
// the library the seed built.
func TestServerInfo(t *testing.T) {
	out := call(t, "server_info", nil)

	if !strings.HasPrefix(str(out["sonarr_version"]), "4.") {
		t.Errorf("sonarr_version = %v, want a Sonarr 4", out["sonarr_version"])
	}
	if out["docker"] != true || str(out["operating_system"]) == "" || str(out["database"]) == "" {
		t.Errorf("server_info = %v", out)
	}
	if got := str(out["sonarr_mcp_version"]); got != version.Version {
		t.Errorf("sonarr_mcp_version = %q, want %q", got, version.Version)
	}
	if n := num(t, out["series"], "series"); n < len(importedSeed)+1 {
		t.Errorf("series = %d, want at least the %d seeded", n, len(importedSeed)+1)
	}
}

func TestServerHealth(t *testing.T) {
	out := call(t, "server_health", nil)

	// errors first; the only one a seeded Sonarr has is the RSS check the
	// suite's indexer setup causes (see TestAuditHealth), and it warns that
	// any host may reach it
	checks := rows(t, out["checks"], "checks")
	for i, c := range checks {
		if str(c["level"]) == "error" && str(c["source"]) != "IndexerRssCheck" {
			t.Errorf("a seeded Sonarr fails %v", c)
		}
		if i > 0 && levelRank(str(c["level"])) < levelRank(str(checks[i-1]["level"])) {
			t.Errorf("not errors first: %v after %v", c["level"], checks[i-1]["level"])
		}
	}
	allowed := findRow(t, checks, "source", "AllowedHostsCheck")
	if str(allowed["level"]) != "warning" || !strings.HasPrefix(str(allowed["wiki_url"]), "https://") {
		t.Errorf("AllowedHostsCheck = %v", allowed)
	}
}

func levelRank(level string) int {
	return map[string]int{"error": 0, "warning": 1, "notice": 2}[level]
}

func TestTasks(t *testing.T) {
	out := call(t, "task_list", nil)

	tasks := rows(t, out["tasks"], "tasks")
	rss := findRow(t, tasks, "command", "RssSync")
	if numOr0(rss["interval_minutes"]) == 0 || str(rss["next_execution"]) == "" {
		t.Errorf("RssSync = %v", rss)
	}

	// by command name, or by the name task_list shows, spaces and case aside
	for _, name := range []string{"CheckHealth", "check health"} {
		run := call(t, "task_run", map[string]any{"task": name})
		if str(run["status"]) != "completed" || numOr0(run["command_id"]) == 0 {
			t.Errorf("task_run %q = %v", name, run)
		}
	}
	if msg := callErr(t, "task_run", map[string]any{"task": "Defragment"}); !strings.Contains(msg, "RssSync") {
		t.Errorf("an unknown task = %s", msg)
	}

	// what just ran is in the command list
	cmds := rows(t, call(t, "command_list", nil)["commands"], "commands")
	if len(cmds) == 0 {
		t.Fatal("command_list is empty after running a task")
	}
	found := false
	for _, c := range cmds {
		if str(c["command"]) == "Check Health" && str(c["status"]) == "completed" {
			found = true
		}
	}
	if !found {
		t.Errorf("Check Health is not among the commands: %v", cmds)
	}
}

func TestLogList(t *testing.T) {
	out := call(t, "log_list", map[string]any{"level": "info", "limit": 5})

	entries := rows(t, out["entries"], "entries")
	if len(entries) == 0 || len(entries) > 5 || num(t, out["total_at_level"], "total") < len(entries) {
		t.Fatalf("log_list = %v", out)
	}
	for i, e := range entries {
		if str(e["time"]) == "" || str(e["logger"]) == "" || str(e["message"]) == "" {
			t.Errorf("entry %d = %v", i, e)
		}
		if i > 0 && str(e["time"]) > str(entries[i-1]["time"]) {
			t.Errorf("not newest first at %d", i)
		}
	}
	// the default is warnings and above
	for _, e := range rows(t, call(t, "log_list", nil)["entries"], "entries") {
		if l := str(e["level"]); l == "info" || l == "debug" || l == "trace" {
			t.Errorf("the default level included %s: %v", l, e)
		}
	}
	if msg := callErr(t, "log_list", map[string]any{"level": "loud"}); !strings.Contains(msg, "level must be one of") {
		t.Errorf("an unknown level = %s", msg)
	}
}
