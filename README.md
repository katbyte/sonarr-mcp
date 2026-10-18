# sonarr-mcp - a Sonarr MCP server, CLI and Go SDK

[![GitHub release](https://img.shields.io/github/v/release/katbyte/sonarr-mcp?color=blueviolet)](https://github.com/katbyte/sonarr-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/sonarr-mcp?color=00ADD8)](https://github.com/katbyte/sonarr-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/sonarr-mcp?color=blue)](https://github.com/katbyte/sonarr-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/sonarr-mcp/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/sonarr-mcp/actions/workflows/pr-acceptance.yaml/badge.svg)
![lint](https://github.com/katbyte/sonarr-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/sonarr-mcp/badges/coverage.json)](https://github.com/katbyte/sonarr-mcp/actions/workflows/coverage.yaml)

sonarr-mcp lets an AI assistant find what has gone wrong in a [Sonarr](https://sonarr.tv) library, and fix it.

**Detection is code, correction is judgment.** The server checks the whole library and hands back a short list of problems, each with the tool that fixes it. The assistant only has to decide what to do.

Install it, and register it with Claude Code:

```bash
go install github.com/katbyte/sonarr-mcp@latest
claude mcp add sonarr -e SONARR_SERVER=http://nas:8989 -e SONARR_TOKEN=... -e SONARR_TOOLSETS=curation -- sonarr-mcp serve
```

Then ask for `audit_all`. It says where the library needs work.

It is an [MCP](https://modelcontextprotocol.io) server, so Claude Desktop and any other MCP client work too.

## The audits

There are 18, and `audit_all` runs them all. Each checks the whole library for one thing that goes wrong.

| audit | what it catches |
|---|---|
| [`audit_all`](docs/audits.md#audit_all) | Runs every audit and gives the counts. Start here. |
| [`audit_missing_episodes`](docs/audits.md#audit_missing_episodes) | Episodes that aired and never arrived. Fix with `episode_search` or `season_search`. |
| [`audit_no_files`](docs/audits.md#audit_no_files) | Series with no files at all, and why. Fix with `series_search` or `import_apply`. |
| [`audit_cutoff_unmet`](docs/audits.md#audit_cutoff_unmet) | Files below the quality their profile asks for. Fix with `series_search`. |
| [`audit_stuck_downloads`](docs/audits.md#audit_stuck_downloads) | Downloads waiting on a person, and Sonarr's reason. Fix with `import_apply` or `queue_remove`. |
| [`audit_failed_downloads`](docs/audits.md#audit_failed_downloads) | Downloads that failed in the last 30 days, or went nowhere. Fix with `release_search`. |
| [`audit_missing_files`](docs/audits.md#audit_missing_files) | Files Sonarr lists that are gone from disk. Fix with `series_rescan`. |
| [`audit_untracked_files`](docs/audits.md#audit_untracked_files) | Video files in a series folder that Sonarr does not track. Fix with `import_apply`. |
| [`audit_unmapped_folders`](docs/audits.md#audit_unmapped_folders) | Folders on disk that are not a series in Sonarr. Fix with `series_import`. |
| [`audit_missing_folders`](docs/audits.md#audit_missing_folders) | Series whose folder was moved, renamed or lost. Fix with `series_edit` and `series_rescan`. |
| [`audit_naming`](docs/audits.md#audit_naming) | Files not named the way Sonarr is set to name them. Fix with `series_rename`. |
| [`audit_quality_mismatch`](docs/audits.md#audit_quality_mismatch) | Files recorded as one quality that are really another. Fix with `file_edit`. |
| [`audit_runtime`](docs/audits.md#audit_runtime) | Files much shorter or longer than the episode. Fix with `release_search`. |
| [`audit_language`](docs/audits.md#audit_language) | Files in the wrong language. Fix with `file_edit` or `release_search`. |
| [`audit_monitoring`](docs/audits.md#audit_monitoring) | Monitoring that will quietly miss new episodes. Fix with `series_edit` or `season_monitor`. |
| [`audit_series_settings`](docs/audits.md#audit_series_settings) | Series set up wrong, such as anime not marked as anime. Fix with `series_edit`. |
| [`audit_profiles`](docs/audits.md#audit_profiles) | Profiles, tags and rules that nothing uses. Fix with `tag_delete`. |
| [`audit_health`](docs/audits.md#audit_health) | Sonarr's own health warnings, and disks running low. |
| [`audit_server`](docs/audits.md#audit_server) | No recent backup, an update waiting, errors in the log. Fix with `backup_create`. |

The rules behind each audit are in [docs/audits.md](docs/audits.md).

## What else is in the box

- **142 tools.** Look through the library, search and grab, import and rename, change Sonarr's settings, and run backups and restarts. See [docs/tools.md](docs/tools.md).
- **Small by default.** Six read-only tools load unless you ask for more.
- **Careful by default.** Nothing can be deleted until you turn deleting on, and each delete then waits to be confirmed.
- **A Go client.** `sdk/sonarr` covers all 239 operations of Sonarr's v3 API, and works on its own. See [docs/sdk.md](docs/sdk.md).
- **Tested against a real Sonarr.** Every tool runs against Sonarr 4.0.20 in Docker. Searches really grab, and downloads really import.

## Configuration

Set these as environment variables, as flags, or in a config file. Only the first two are required.

| Variable | Flag | What it does |
|---|---|---|
| `SONARR_SERVER` | `--server` | Sonarr's address, such as `http://nas:8989`. |
| `SONARR_TOKEN` | `--token` | Sonarr's API key, from Settings → General → Security. |
| `SONARR_TOOLSETS` | `--toolsets` | Which groups of tools load. See [Tools](#tools). |
| `SONARR_READ_ONLY` | `--read-only` | Load only the tools that change nothing. |
| `SONARR_ENABLE_DELETE` | `--enable-delete` | Load the tools that delete. |
| `SONARR_ALLOW_TOOLS` | `--allow-tools` | Load these tools, by name. |
| `SONARR_DENY_TOOLS` | `--deny-tools` | Never load these tools. |
| `SONARR_LISTEN` | `--listen` | Serve over HTTP on this address. |
| `SONARR_AUTH_TOKEN` | `--auth-token` | The token HTTP clients must send. |
| `SONARR_ALLOW_NO_AUTH` | `--allow-no-auth` | Serve HTTP with no token, open to anyone who can reach it. |
| `SONARR_LOG` | | How much to log: `WARN` (the default), `INFO` or `TRACE`. |

`SONARR_URL` and `SONARR_API_KEY` work too, since other Sonarr tools use those names.

A config file goes in `~/.sonarr-mcp`, or in `./.sonarr-mcp` for one project. Its names are the flags in capitals, with `_` for `-` and without `SONARR_` in front, such as `ALLOW_TOOLS`. A name it does not take stops the program, saying what to write instead:

```env
SERVER=http://nas:8989
TOKEN=0123456789abcdef...
TOOLSETS=curation
```

## Usage

Check the connection:

```bash
sonarr-mcp info
```

**In a project's `.mcp.json`:**

```json
{
  "mcpServers": {
    "sonarr": {
      "command": "sonarr-mcp",
      "args": ["serve"],
      "env": {
        "SONARR_SERVER": "http://nas:8989",
        "SONARR_TOKEN": "...",
        "SONARR_TOOLSETS": "curation"
      }
    }
  }
}
```

**Over HTTP.** `sonarr-mcp serve --listen :8080` serves at `/mcp`, so one server can be shared between machines. It will not start without `SONARR_AUTH_TOKEN`, which clients send as a bearer token:

```bash
claude mcp add --transport http sonarr http://nas:8080/mcp --header "Authorization: Bearer $SONARR_AUTH_TOKEN"
```

**In Docker.** Each release publishes an image to `ghcr.io/katbyte/sonarr-mcp`, and `docker-compose.yml` runs it as an HTTP server. Set `SONARR_SERVER` and `TZ` in that file, then:

```bash
cp .env.example .env      # fill in SONARR_TOKEN and SONARR_AUTH_TOKEN
docker compose up -d
```

## Tools

Only `core` loads by default. Every tool the assistant is given takes room it could use to think, so load the groups a session needs.

| toolset | what it is for | tools | ~tokens |
|---|---|---|---|
| `core` | Find and read series, episodes and the queue. | 6 | 1,000 |
| `library` | Add shows and root folders. | 11 | 1,800 |
| `curation` | Every audit, and what fixes their findings. | 54 | 9,500 |
| `admin` | Health, logs, backups and restarts. | 26 | 3,500 |
| `settings` | Set Sonarr up: indexers, download clients, profiles, rules. | 54 | 10,400 |
| `all` | Everything. | 127 | 22,100 |

Counts include `core`, which always loads. The 15 tools that delete are extra, and load only with `--enable-delete`.

The guard rails:

- **Read-only.** `--read-only` loads the 69 tools that change nothing. The rest are not there to be called.
- **Deletes.** Without `confirm`, a delete tool changes nothing and answers what it would delete.
- **Risky changes.** A restart, a shutdown, restoring a backup and changing the host's settings wait for `confirm` too.
- **Credentials.** They can be set, and are never shown.
- **Text from outside.** Release names, plots and download client messages are cleaned of hidden characters and cut short.

## More

- [docs/audits.md](docs/audits.md): what each audit looks for, and every kind of problem it reports.
- [docs/tools.md](docs/tools.md): every tool, and the rules they all follow.
- [docs/toolsets.md](docs/toolsets.md): choosing which tools load, down to single tools.
- [docs/curation.md](docs/curation.md): a typical session, one audit at a time.
- [docs/sdk.md](docs/sdk.md): using the Go client on its own, and how it is made.
- [docs/development.md](docs/development.md): building, the tests and the live suites.
- [docs/ROADMAP.md](docs/ROADMAP.md): what is done and what is next.
- [CONTRIBUTING.md](CONTRIBUTING.md): checking a change, and how releases go out.
