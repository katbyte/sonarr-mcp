package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/sonarr-mcp/lib/sonarr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerServerTools(r *registry) {
	client := r.client

	// serverInfoOut names both versions, because both change what a caller
	// can rely on: Sonarr's decides what the API answers, and this binary's
	// decides which tools and fixes are in play. The second is the one that
	// goes stale unnoticed - an MCP client keeps the binary it started with,
	// so a session can run for hours on a build that predates the fix it is
	// relying on, and nothing else it can call would say so.
	type serverInfoOut struct {
		InstanceName    string `json:"instance_name"`
		Version         string `json:"sonarr_version"`
		OperatingSystem string `json:"operating_system"`
		Docker          bool   `json:"docker"`
		Branch          string `json:"branch,omitempty"`
		StartTime       string `json:"start_time"`
		URLBase         string `json:"url_base,omitempty"`
		Database        string `json:"database"`
		Series          int    `json:"series"`
		HealthErrors    int    `json:"health_errors"      jsonschema:"health checks Sonarr is failing at error level"`
		HealthWarnings  int    `json:"health_warnings"    jsonschema:"health checks at warning level"`
		SonarrMCP       string `json:"sonarr_mcp_version" jsonschema:"the build of this MCP server answering, e.g. v0.1.0+4@g8909c7c: the tag, the commits since it, and the commit"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_info",
		Description: "Check the connection to Sonarr and return its version, how many series it holds, how many health checks it is failing, and the version of this MCP server.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, serverInfoOut, error) {
		st, err := client.GetSystemStatus(ctx)
		if err != nil {
			return nil, serverInfoOut{}, err
		}
		health, err := client.GetHealth(ctx)
		if err != nil {
			return nil, serverInfoOut{}, err
		}
		series, err := r.allSeries(ctx)
		if err != nil {
			return nil, serverInfoOut{}, err
		}

		s := st.Model
		out := serverInfoOut{
			InstanceName:    s.InstanceName,
			Version:         s.Version,
			OperatingSystem: strings.TrimSpace(s.OsName + " " + s.OsVersion),
			Docker:          boolv(s.IsDocker),
			Branch:          s.Branch,
			StartTime:       s.StartTime,
			URLBase:         s.UrlBase,
			Database:        strings.TrimSpace(string(s.DatabaseType) + " " + s.DatabaseVersion),
			Series:          len(series),
			SonarrMCP:       version.Version,
		}
		for _, h := range health.Model {
			switch h.Type {
			case sonarr.HealthCheckResultError:
				out.HealthErrors++
			case sonarr.HealthCheckResultWarning:
				out.HealthWarnings++
			default:
			}
		}

		return nil, out, nil
	})

	type healthRow struct {
		Level   string `json:"level"              jsonschema:"error, warning or notice"`
		Source  string `json:"source"             jsonschema:"the check that raised it, e.g. IndexerStatusCheck"`
		Message string `json:"message"`
		WikiURL string `json:"wiki_url,omitempty"`
	}
	type healthOut struct {
		Checks []healthRow `json:"checks" jsonschema:"errors first; empty when Sonarr reports nothing wrong"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "server_health",
		Description: "Sonarr's own health checks, errors first: indexers or download clients failing, root folders missing, a download client's files Sonarr cannot see, an update it wants. Empty when Sonarr is healthy.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, healthOut, error) {
		health, err := client.GetHealth(ctx)
		if err != nil {
			return nil, healthOut{}, err
		}
		out := healthOut{}
		for _, h := range health.Model {
			row := healthRow{Level: string(h.Type), Source: h.Source, Message: h.Message, WikiURL: h.WikiUrl}
			out.Checks = append(out.Checks, row)
		}
		slices.SortStableFunc(out.Checks, func(a, b healthRow) int { return healthRank(a.Level) - healthRank(b.Level) })

		return nil, out, nil
	})

	type taskRow struct {
		Name            string `json:"name"`
		Command         string `json:"command"                  jsonschema:"what task_run takes to run it now"`
		IntervalMinutes int    `json:"interval_minutes"`
		LastExecution   string `json:"last_execution,omitempty"`
		LastDuration    string `json:"last_duration,omitempty"`
		NextExecution   string `json:"next_execution,omitempty"`
	}
	type tasksOut struct {
		Tasks []taskRow `json:"tasks"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "task_list",
		Description: "Sonarr's scheduled tasks - RSS sync, refreshing series, checking downloads, backups, housekeeping - with when each last ran, how long it took, and when it runs next.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, tasksOut, error) {
		res, err := client.GetSystemTask(ctx)
		if err != nil {
			return nil, tasksOut{}, err
		}
		out := tasksOut{}
		for _, t := range res.Model {
			out.Tasks = append(out.Tasks, taskRow{
				Name: t.Name, Command: t.TaskName, IntervalMinutes: t.Interval,
				LastExecution: t.LastExecution, LastDuration: t.LastDuration, NextExecution: t.NextExecution,
			})
		}
		slices.SortFunc(out.Tasks, func(a, b taskRow) int { return strings.Compare(a.Name, b.Name) })

		return nil, out, nil
	})

	type taskRunIn struct {
		Task string `json:"task"                   jsonschema:"the task to run now, by name or command as task_list shows them, e.g. RssSync, RefreshMonitoredDownloads, CheckHealth, Backup"`
		Wait int    `json:"wait_seconds,omitempty" jsonschema:"how long to wait for it to finish, default 60; -1 queues it and returns at once"`
	}
	add(r, writeTool, &mcp.Tool{
		Name:        "task_run",
		Description: "Run one of Sonarr's scheduled tasks now rather than when it is next due: an RSS sync, a check of the download clients, a health check, a backup. Waits for it to finish by default.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in taskRunIn) (*mcp.CallToolResult, commandOut, error) {
		res, err := client.GetSystemTask(ctx)
		if err != nil {
			return nil, commandOut{}, err
		}
		want := strings.ToLower(strings.ReplaceAll(in.Task, " ", ""))
		var known []string
		for _, t := range res.Model {
			known = append(known, t.TaskName)
			if strings.EqualFold(t.TaskName, want) || strings.EqualFold(strings.ReplaceAll(t.Name, " ", ""), want) {
				out, err := r.runCommand(ctx, t.TaskName, nil, waitFor(in.Wait))
				return nil, out, err
			}
		}
		slices.Sort(known)

		return nil, commandOut{}, fmt.Errorf("no scheduled task %q (have: %s)", in.Task, strings.Join(known, ", "))
	})

	type commandsOut struct {
		Commands []commandOut `json:"commands" jsonschema:"running and queued first, then the recently finished"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "command_list",
		Description: "What Sonarr is doing and has just done: the commands running, queued and recently finished - searches, refreshes, imports, renames - with their outcome and message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, commandsOut, error) {
		res, err := client.GetCommand(ctx)
		if err != nil {
			return nil, commandsOut{}, err
		}
		out := commandsOut{}
		for i := range res.Model {
			out.Commands = append(out.Commands, projectCommand(&res.Model[i]))
		}

		return nil, out, nil
	})

	type logIn struct {
		Level string `json:"level,omitempty" jsonschema:"the least severe level to include: trace, debug, info, warn (default), error or fatal"`
		Limit int    `json:"limit,omitempty" jsonschema:"entries to return, newest first, default 50"`
	}
	type logRow struct {
		Time      string `json:"time"`
		Level     string `json:"level"`
		Logger    string `json:"logger"`
		Message   string `json:"message"`
		Exception string `json:"exception,omitempty" jsonschema:"the first line of the exception, when there was one"`
	}
	type logOut struct {
		Total   int      `json:"total_at_level" jsonschema:"entries at this level and above in Sonarr's log table"`
		Entries []logRow `json:"entries"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "log_list",
		Description: "Sonarr's recent log entries, newest first, warnings and above by default: the failed imports, rejected grabs and unreachable indexers that explain what a health check or a stuck download only summarises.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in logIn) (*mcp.CallToolResult, logOut, error) {
		level := strings.ToLower(strings.TrimSpace(in.Level))
		if level == "" {
			level = "warn"
		}
		if !slices.Contains(logLevels, level) {
			return nil, logOut{}, fmt.Errorf("level must be one of %s, got %q", strings.Join(logLevels, ", "), in.Level)
		}
		res, err := client.GetLog(ctx, sonarr.GetLogOperationOptions{
			Page: 1, PageSize: limitOr(in.Limit, 50), SortKey: "time", SortDirection: sonarr.SortDirectionDescending, Level: level,
		})
		if err != nil {
			return nil, logOut{}, err
		}
		out := logOut{Total: res.Model.TotalRecords}
		for _, e := range res.Model.Records {
			out.Entries = append(out.Entries, logRow{
				Time: e.Time, Level: e.Level, Logger: e.Logger, Message: e.Message, Exception: firstLine(e.Exception),
			})
		}

		return nil, out, nil
	})

	type configIn struct {
		Section string `json:"section" jsonschema:"naming, mediamanagement, host, ui, indexer, downloadclient or importlist"`
	}
	type configOut struct {
		Section  string         `json:"section"`
		Settings map[string]any `json:"settings" jsonschema:"the section as Sonarr holds it; credentials are left out"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "config_get",
		Description: "Read one section of Sonarr's settings: the episode and folder naming formats, media management (hardlinks, recycling bin, permissions, what happens to deleted files), host, UI, and the indexer, download client and import list options. Credentials are never returned.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in configIn) (*mcp.CallToolResult, configOut, error) {
		section := strings.ToLower(strings.TrimSpace(in.Section))
		var body any
		var err error
		switch section {
		case "naming":
			var res sonarr.GetConfigNamingOperationResponse
			res, err = client.GetConfigNaming(ctx)
			body = res.Model
		case "mediamanagement", "media_management", "media management":
			section = "mediamanagement"
			var res sonarr.GetConfigMediaManagementOperationResponse
			res, err = client.GetConfigMediaManagement(ctx)
			body = res.Model
		case "host":
			var res sonarr.GetConfigHostOperationResponse
			res, err = client.GetConfigHost(ctx)
			body = res.Model
		case "ui":
			var res sonarr.GetConfigUiOperationResponse
			res, err = client.GetConfigUi(ctx)
			body = res.Model
		case "indexer":
			var res sonarr.GetConfigIndexerOperationResponse
			res, err = client.GetConfigIndexer(ctx)
			body = res.Model
		case "downloadclient", "download_client":
			section = "downloadclient"
			var res sonarr.GetConfigDownloadClientOperationResponse
			res, err = client.GetConfigDownloadClient(ctx)
			body = res.Model
		case "importlist", "import_list":
			section = "importlist"
			var res sonarr.GetConfigImportListOperationResponse
			res, err = client.GetConfigImportList(ctx)
			body = res.Model
		default:
			return nil, configOut{}, fmt.Errorf("section must be naming, mediamanagement, host, ui, indexer, downloadclient or importlist, got %q", in.Section)
		}
		if err != nil {
			return nil, configOut{}, err
		}
		settings, err := asMap(body)
		if err != nil {
			return nil, configOut{}, err
		}
		for k := range settings {
			if secretSetting(k) {
				delete(settings, k)
			}
		}

		return nil, configOut{Section: section, Settings: settings}, nil
	})
}

// logLevels are Sonarr's log levels, least severe first.
var logLevels = []string{"trace", "debug", "info", "warn", "error", "fatal"}

// healthRank orders health levels most severe first.
func healthRank(level string) int {
	switch sonarr.HealthCheckResult(level) {
	case sonarr.HealthCheckResultError:
		return 0
	case sonarr.HealthCheckResultWarning:
		return 1
	case sonarr.HealthCheckResultNotice:
		return 2
	default:
		return 3
	}
}

// secretSetting reports whether a settings key holds a credential: the API
// key, a password, a certificate's password, a proxy's.
func secretSetting(key string) bool {
	k := strings.ToLower(key)

	return k == "apikey" || strings.Contains(k, "password") || strings.HasSuffix(k, "secret")
}

// asMap renders a model as a JSON object, for a caller that wants its fields
// by name.
func asMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}

	return out, nil
}
