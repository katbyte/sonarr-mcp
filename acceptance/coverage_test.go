//go:build integration

package acceptance

import (
	"slices"
	"strings"
	"sync"

	"github.com/katbyte/sonarr-mcp/tools"
)

// Audit coverage: every kind of finding an audit can report (tools.AuditProblems)
// has to be reported by some test against the seeded library, or be named
// below with the reason it cannot be. A whole run that reports none of a kind
// fails, so an audit's branch cannot quietly go untested - the same rule the
// suite applies to the tools themselves.

// notReported are the findings the library cannot produce, by "audit: kind",
// each with why. A kind here that a run reports after all fails too, so the
// list cannot go stale.
var notReported = map[string]string{
	// a grab counts as lost six hours after it was sent, and a download as
	// stuck one hour (import) or a day (never started) after it arrived:
	// nothing in a two minute run is that old
	"audit_failed_downloads: grab went nowhere": "a grab is lost only six hours after it was sent; TestAuditFailedDownloads covers it",
	"audit_stuck_downloads: waiting to import":  "an import is late only an hour after the download finished; TestStuckProblem covers it",
	"audit_stuck_downloads: not starting":       "a download is stalled only a day after it was queued; TestStuckProblem covers it",
	// what the container cannot be made to be
	"audit_health: root folder low on space":                      "the container's disk is not nearly full; TestAuditHealth covers it",
	"audit_health: root folder unreachable":                       "Sonarr will not take a root folder it cannot read; TestAuditHealth covers it",
	"audit_missing_folders: root folder holds none of its series": "it would mean moving the whole library aside; TestAuditFoldersEmptyRoot covers it",
	// what the fixtures are not
	"audit_runtime: longer than it should be":           "the fixture videos are made to their episode's length; TestAuditRuntimeEdges covers it",
	"audit_runtime: no running time":                    "every fixture video has a running time Sonarr can read; TestAuditRuntimeEdges covers it",
	"audit_series_settings: daily show not typed daily": "no fixture series is a talk show or the news; TestAuditSeriesSettingsDaily covers it",
	"audit_untracked_files: file not tracked":           "Sonarr's scan accounts for every video file in a series folder, so one it will not account for cannot be arranged; TestAuditUntrackedFileSonarrIgnores covers it",
	"audit_language: language unknown":                  "Sonarr records a language for every fixture file, and this is a file it could not tell; TestAuditLanguageEdges covers it",
	// what Sonarr 4 does not do
	"audit_stuck_downloads: download failed": "Sonarr blocklists a failed download and drops it from the queue as it handles it, even with the client told to keep the download, so the queue never holds one; TestStuckProblem covers it",
	// what would break the rest of the run
	"audit_stuck_downloads: download client unreachable": "Sonarr backs off from a client that fails, for minutes, which would stop the downloads the rest of the suite makes; TestAuditStuckDownloads covers it",
	"audit_stuck_downloads: error":                       "Sonarr flags a tracked download as errored only for failures it cannot recover from, which the fake client cannot cause; TestStuckProblem covers it",
	"audit_stuck_downloads: warning":                     "a download still going with a warning on it; the fake client's warnings all arrive as an import that cannot be made; TestStuckProblem covers it",
}

var (
	seenMu   sync.Mutex
	seenKind = map[string]bool{}
)

// recordFindings notes the kinds of finding an audit reported.
func recordFindings(name string, out map[string]any) {
	if !strings.HasPrefix(name, "audit_") {
		return
	}
	rows, ok := out["findings"].([]any)
	if !ok {
		return
	}

	seenMu.Lock()
	defer seenMu.Unlock()
	for _, row := range rows {
		f, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if problem, ok := f["problem"].(string); ok {
			seenKind[name+": "+problem] = true
		}
	}
}

// reported reports whether a run has seen a kind of finding. audit_health's
// kinds are the start of a finding that ends in the check's own level, so a
// kind matches what starts with it.
func reported(audit, kind string) bool {
	seenMu.Lock()
	defer seenMu.Unlock()

	for seen := range seenKind {
		if seen == audit+": "+kind || strings.HasPrefix(seen, audit+": "+kind+" ") {
			return true
		}
	}

	return false
}

// unreportedFindings names the kinds of finding this run never reported and
// nothing excuses, and the excuses that turned out to be wrong.
func unreportedFindings() (missing, stale []string) {
	for audit, kinds := range tools.AuditProblems {
		for _, kind := range kinds {
			key := audit + ": " + kind
			switch {
			case reported(audit, kind):
				if notReported[key] != "" {
					stale = append(stale, key)
				}
			case notReported[key] == "":
				missing = append(missing, key)
			}
		}
	}
	slices.Sort(missing)
	slices.Sort(stale)

	return missing, stale
}
