package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/katbyte/go-kt/version"
	apiclient "github.com/katbyte/pandorest/client"
	"github.com/katbyte/sonarr-mcp/sdk/sonarr"
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

		s := st.Answer
		out := serverInfoOut{
			InstanceName:    s.InstanceName,
			Version:         s.Version,
			OperatingSystem: strings.TrimSpace(s.OsName + " " + s.OsVersion),
			Docker:          s.IsDocker,
			Branch:          s.Branch,
			StartTime:       stamp(s.StartTime.Time),
			URLBase:         s.UrlBase,
			Database:        strings.TrimSpace(string(s.DatabaseType) + " " + s.DatabaseVersion),
			Series:          len(series),
			SonarrMCP:       version.Version,
		}
		for _, h := range health.Answer {
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
		for _, h := range health.Answer {
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
		pageOut
		Tasks []taskRow `json:"tasks"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "task_list",
		Description: "Sonarr's scheduled tasks - RSS sync, refreshing series, checking downloads, backups, housekeeping - with when each last ran, how long it took, and when it runs next.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in pageIn) (*mcp.CallToolResult, tasksOut, error) {
		res, err := client.GetSystemTask(ctx)
		if err != nil {
			return nil, tasksOut{}, err
		}
		rows := make([]taskRow, 0, len(res.Answer))
		for _, t := range res.Answer {
			rows = append(rows, taskRow{
				Name: t.Name, Command: t.TaskName, IntervalMinutes: t.Interval,
				LastExecution: stamp(t.LastExecution.Time), LastDuration: t.LastDuration, NextExecution: stamp(t.NextExecution.Time),
			})
		}
		slices.SortFunc(rows, func(a, b taskRow) int { return strings.Compare(a.Name, b.Name) })
		out := tasksOut{}
		out.Tasks, out.pageOut = pageRows(rows, in)

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
		for _, t := range res.Answer {
			known = append(known, t.TaskName)
			if strings.EqualFold(t.TaskName, want) || strings.EqualFold(strings.ReplaceAll(t.Name, " ", ""), want) {
				// a task is queued by the name Sonarr lists it under, which no method knows ahead of time
				body, err := json.Marshal(map[string]string{"name": t.TaskName})
				if err != nil {
					return nil, commandOut{}, err
				}
				sent, err := client.PostCommand(ctx, body)
				out, err := r.settleCommand(ctx, t.TaskName, waitFor(in.Wait), sent, err)
				return nil, out, err
			}
		}
		slices.Sort(known)

		return nil, commandOut{}, fmt.Errorf("no scheduled task %q (have: %s)", in.Task, strings.Join(known, ", "))
	})

	type commandsIn struct {
		CommandID sonarr.CommandID `json:"command_id,omitempty" jsonschema:"only this command, by the command_id a tool that started it answered: how a search, refresh or rename still running when its wait ended is followed"`
		pageIn
	}
	type commandsOut struct {
		pageOut
		Commands []commandOut `json:"commands" jsonschema:"running and queued first, then the recently finished"`
	}
	add(r, readTool, &mcp.Tool{
		Name:        "command_list",
		Description: "What Sonarr is doing and has just done: the commands running, queued and recently finished - searches, refreshes, imports, renames - with their outcome and message. Sonarr runs housekeeping of its own every minute, so follow a command a tool started by its command_id rather than looking for it in the list.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in commandsIn) (*mcp.CallToolResult, commandsOut, error) {
		if in.CommandID != 0 {
			one, err := client.GetCommandById(ctx, in.CommandID)
			switch {
			case apiclient.IsNotFound(err):
				return nil, commandsOut{}, fmt.Errorf("sonarr has no command %d: it drops finished commands after a while, so check the id", in.CommandID)
			case err != nil:
				return nil, commandsOut{}, err
			}
			return nil, commandsOut{Total: 1, Commands: []commandOut{projectCommand(one.Answer)}}, nil
		}

		res, err := client.GetCommand(ctx)
		if err != nil {
			return nil, commandsOut{}, err
		}
		// Sonarr answers oldest first, and a busy one has run hundreds in the
		// minutes it remembers: what is running or waiting comes first, then
		// what finished, newest first, so the first page is the one wanted
		slices.SortStableFunc(res.Answer, func(a, b sonarr.CommandResource) int {
			if doneA, doneB := commandDone(a.Status), commandDone(b.Status); doneA != doneB {
				if doneB {
					return -1
				}
				return 1
			}
			return cmp.Compare(b.Id, a.Id)
		})
		commands, paged := pageRows(res.Answer, in.pageIn)
		out := commandsOut{pageOut: paged, Commands: make([]commandOut, 0, len(commands))}
		for i := range commands {
			out.Commands = append(out.Commands, projectCommand(&commands[i]))
		}

		return nil, out, nil
	})

	type logIn struct {
		Level string `json:"level,omitempty" jsonschema:"the least severe level to include: trace, debug, info, warn (default), error or fatal"`
		pageIn
	}
	type logRow struct {
		Time      string `json:"time"`
		Level     string `json:"level"`
		Logger    string `json:"logger"`
		Message   string `json:"message"`
		Exception string `json:"exception,omitempty" jsonschema:"the first line of the exception, when there was one"`
	}
	type logOut struct {
		pageOut
		Entries []logRow `json:"entries" jsonschema:"newest first"`
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
		// Sonarr pages its log table itself, and total is the entries at this
		// level and above in it
		records, paged, err := window(in.pageIn, func(page, size int) ([]sonarr.LogResource, int, error) {
			res, err := client.GetLog(ctx, sonarr.GetLogOptions{Page: new(page), PageSize: new(size), SortKey: "time", SortDirection: sonarr.SortDirectionDescending, Level: level})
			if err != nil {
				return nil, 0, err
			}

			return res.Answer.Records, res.Answer.TotalRecords, nil
		})
		if err != nil {
			return nil, logOut{}, err
		}
		out := logOut{pageOut: paged, Entries: make([]logRow, 0, len(records))}
		for _, e := range records {
			out.Entries = append(out.Entries, logRow{
				Time: stamp(e.Time.Time), Level: e.Level, Logger: e.Logger, Message: e.Message, Exception: firstLine(e.Exception),
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
		sec, err := r.findSection(in.Section)
		if err != nil {
			return nil, configOut{}, err
		}
		settings, err := sec.read(ctx)
		if err != nil {
			return nil, configOut{}, err
		}

		return nil, configOut{Section: sec.name, Settings: withoutSecrets(settings)}, nil
	})

	registerConfigEdit(r)
	registerNamingPreview(r)
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

// secretEndings are how the name of a setting that holds a credential ends,
// lowercased, whatever is in front: apiKey, passkey and userKey, accessToken
// and botToken, appSecret, a tracker's cookie.
var secretEndings = []string{"key", "token", "secret", "passphrase", "cookie"}

// secretAddresses are the settings that are an address which is itself the
// credential, lowercased: whoever holds a Discord or Slack webhook's address
// can post to it, and each of Apprise's addresses carries its service's
// token in its host or its path (discord://id/token), where taking the
// sign-in and the parameters out of an address does nothing.
var secretAddresses = []string{"webhookurl", "statelessurls"}

// secretSetting reports whether a setting holds a credential, by its name:
// a password wherever the word is in it, a name that ends as a credential's
// does, or an address that is one. It goes by the name because one left off
// a list is a credential in an answer with nothing to say so; the price is
// a setting that is no secret and is not shown, which can still be set.
func secretSetting(key string) bool {
	k := strings.ToLower(key)

	return strings.Contains(k, "password") || slices.Contains(secretAddresses, k) || slices.ContainsFunc(secretEndings, func(ending string) bool { return strings.HasSuffix(k, ending) })
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
