# sonarr-mcp - a Sonarr MCP server, CLI and Go SDK

[![GitHub release](https://img.shields.io/github/v/release/katbyte/sonarr-mcp?color=blueviolet)](https://github.com/katbyte/sonarr-mcp/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/sonarr-mcp?color=00ADD8)](https://github.com/katbyte/sonarr-mcp/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/sonarr-mcp?color=blue)](https://github.com/katbyte/sonarr-mcp/blob/main/LICENSE)
![build](https://github.com/katbyte/sonarr-mcp/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/sonarr-mcp/actions/workflows/pr-integration.yaml/badge.svg)
![lint](https://github.com/katbyte/sonarr-mcp/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/sonarr-mcp/badges/coverage.json)](https://github.com/katbyte/sonarr-mcp/actions/workflows/coverage.yaml)

An [MCP](https://modelcontextprotocol.io) server, CLI and Go SDK that **audit a
[Sonarr](https://sonarr.tv) library for the things that actually go wrong, and fix what they
find** - from Claude Code, Claude Desktop, or any other MCP client.

Sonarr's API is large, and an MCP server that wraps it lets a model list your series and
read the queue. This one does that too, but the reason it exists is the layer above: **16
audits**, each a sweep over the whole library for one specific thing that goes wrong with a
real Sonarr - episodes that aired and never arrived, files stuck below their profile's
cutoff, a download blocked from importing since Tuesday, a release that keeps failing, files
deleted behind Sonarr's back, a folder Sonarr has never heard of, a "1080p" file that is
really 720p, an anime typed as standard so its absolute numbers never match, a tag nothing
uses - returning a worklist rather than a dump, and naming the tool that fixes it.

### The audits

| audit | what it catches |
|---|---|
| `audit_all` | every audit in one call, counts only, so one call says where the library needs work - start here |
| `audit_missing_episodes` | monitored episodes that have aired with no file, by series and season, saying which are already downloading and which Sonarr searched for and did not find; `episode_search`, `season_search` or `wanted_search` fetch them |
| `audit_cutoff_unmet` | monitored files below their profile's cutoff: below the cutoff quality, or below the custom format score the profile upgrades until - which Sonarr's own Cutoff Unmet list leaves out. Says when a profile has Upgrades Allowed off (Sonarr's built-in profiles do), so nothing would be replaced by itself; `series_search` or `wanted_search kind=cutoff` go and get them |
| `audit_stuck_downloads` | downloads that need a person: blocked from importing (and Sonarr's reason), waiting to import for hours, failed, paused, warned about, or on a download client Sonarr cannot reach; `import_apply` or `queue_remove` clear them |
| `audit_failed_downloads` | failures over the last 30 days by episode, flagging episodes whose releases keep failing, and grabs that went nowhere - sent to the client, never imported, never failed, gone from the queue |
| `audit_missing_files` | files Sonarr records that are no longer on disk, so the episodes show as downloaded when they are not; `series_rescan` drops the records |
| `audit_untracked_files` | video files in series folders Sonarr is not tracking - copied in by hand, a second copy, a name it cannot parse - with what Sonarr reads from each name and why it has not taken it in; `import_apply` takes them in |
| `audit_unmapped_folders` | folders in the root folders that are not a series in Sonarr, and how many video files each holds: shows copied in by hand, or left behind by a deleted series; `series_import` adds them where they are. A folder that goes by the name of a series whose own folder is gone is named as that series moved, to point at rather than add again |
| `audit_missing_folders` | series whose folder is not on disk: renamed or moved outside Sonarr, or a drive not mounted (reported once for the root folder, not once per series). When a folder Sonarr doesn't know goes by the series' name, the finding names it; `series_edit path` and `series_rescan` fix it |
| `audit_naming` | series whose files are not named to the naming format, with a sample of the renames; `series_rename` renames them. With Rename Episodes off, Sonarr keeps the names files arrive with and cannot say which differ, and the audit says so |
| `audit_quality_mismatch` | files recorded as one quality whose video is another: recorded 1080p but really 720p (never upgraded), or labelled SD but really HD (upgraded forever); `file_edit` corrects the record |
| `audit_runtime` | files whose running time is far off the episode's - a truncated download, a sample imported as the episode, the wrong episode - or that have no running time at all |
| `audit_language` | files not in the language they should be: audio only in other languages, a file recorded as a dub, or one whose language Sonarr could not tell |
| `audit_monitoring` | monitoring that will quietly miss episodes: continuing series that ignore new seasons, unmonitored continuing series, monitored series with every season unmonitored, the latest season left unmonitored |
| `audit_series_settings` | anime not typed anime (absolute numbering fails), daily shows not typed daily, a folder name that does not match the naming format, series outside every root folder; `series_edit` fixes them |
| `audit_profiles` | settings that do nothing: quality profiles no series uses, tags nothing uses, custom formats no profile scores, and delay and release profiles, indexers and download clients scoped to tags no series has |
| `audit_health` | Sonarr's own health checks - indexers and download clients failing, root folders missing, remote path mappings wrong - and root folders running out of space |

The design principle: **detection is code, correction is judgment.** The server runs cheap
deterministic checks over the whole library and produces worklists; the model reasons only
about the anomalies. Every response is a trimmed projection of what a decision needs, never
the raw API object (Sonarr's series resource has 45 fields and nests the seasons, statistics
and images inside it; `series_list` rows carry what a list needs).

### What else is in the box

- **69 tools, in toolsets.** List and inspect series, seasons and episodes, what airs next,
  the queue and history, search and grab, parse a release name, import files and folders,
  rename, rescan and refresh, edit series and files, monitoring, tags, profiles, root folders,
  indexers and download clients (and test them), tasks, commands and logs. Each sits in a
  toolset a session can load on its own, so a client spends under a thousand tokens of
  context by default rather than nine thousand.
- **A Go SDK.** `lib/sonarr` is a complete typed client for Sonarr's v3 API - all 228
  operations, generated from Sonarr's own OpenAPI document, standard library only, no
  knowledge of MCP. Useful on its own, whether or not you care about AI.
- **Tested against a real Sonarr.** Every tool runs against Sonarr in Docker, with a fake
  indexer and a fake SABnzbd the suite drives, so a search really grabs, a download really
  completes and an import really lands on disk. The suite fails if a registered tool has no
  test, and Sonarr's calls out to TheTVDB and its own services are recorded once and replayed,
  so CI needs no network.

## Installation

```bash
go install github.com/katbyte/sonarr-mcp@latest
```

Tested against Sonarr 4.0.20, the version the live suites run.

## Configuration

All options can be passed as command-line flags, environment variables, or via a configuration file.

| Variable | Flag | Description |
|---|---|---|
| `SONARR_SERVER` (or `SONARR_URL`) | `--server`, `-s` | Sonarr's URL, with its URL base if it has one, e.g. `http://nas:8989` |
| `SONARR_TOKEN` (or `SONARR_API_KEY`) | `--token`, `-t` | the API key, from Settings → General → Security |
| `SONARR_READ_ONLY` | `--read-only` | register only tools that never change Sonarr's state |
| `SONARR_ENABLE_DELETE` | `--enable-delete` | register `series_delete` (optionally with its files) and `file_delete`, which delete from disk |
| `SONARR_TOOLSETS` | `--toolsets` | groups of tools to register, default `core`: `all`, `core`, `curation`, `library`, `admin`, or a resource family like `series` (`core` is always included) |
| `SONARR_ALLOW_TOOLS` | `--allow-tools` | only register these tools (names, `series_*` globs, or `essential`) |
| `SONARR_DENY_TOOLS` | `--deny-tools` | never register these tools (names or globs such as `*_delete`) |
| `SONARR_LOG` | | log level (`WARN` default; `DEBUG`, `TRACE`, ...) |
| `SONARR_LISTEN` | `--listen` | serve MCP over HTTP on this address (e.g. `:8080`) instead of stdio |
| `SONARR_AUTH_TOKEN` | `--auth-token` | bearer token required on the HTTP endpoint (required with `--listen`) |
| `SONARR_ALLOW_NO_AUTH` | `--allow-no-auth` | serve HTTP with no bearer token at all: anyone who can reach the port can use every tool |

`SONARR_URL` and `SONARR_API_KEY` are what most other Sonarr tooling reads, so a shell that
already exports them works as it is.

### Configuration File

You can place a `.sonarr-mcp` file in your home directory `~/.sonarr-mcp` (for global settings)
or in your current directory `./.sonarr-mcp` (for per-project settings, which win). Keys match
the long flag names using the `env` format:

```env
SERVER=http://nas:8989
TOKEN=0123456789abcdef...
TOOLSETS=curation
```

## Usage

Quick connectivity check:

```bash
sonarr-mcp info
```

### Register with Claude Code

`.mcp.json`:

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

Or from the shell:

```bash
claude mcp add sonarr -e SONARR_SERVER=http://nas:8989 -e SONARR_TOKEN=... -- sonarr-mcp serve
```

### Run as a service (HTTP transport)

`serve --listen :8080` serves the MCP Streamable HTTP transport at `/mcp` (plus `GET /healthz`)
instead of stdio. `SONARR_AUTH_TOKEN` is required: clients must send `Authorization: Bearer
<token>`, and the server refuses to start without one unless `SONARR_ALLOW_NO_AUTH=true` says
that anyone who can reach the port may use every tool. Register it from any machine:

```bash
claude mcp add --transport http sonarr http://nas:8080/mcp \
  --header "Authorization: Bearer $SONARR_AUTH_TOKEN"
```

### Docker

Releases publish a multi-arch (amd64, arm64) image to `ghcr.io/katbyte/sonarr-mcp`, tagged
`vX.Y.Z`, `vX.Y` and `latest`. `docker-compose.yml` is the default always-on deployment: it runs
that image and reads secrets from a gitignored `.env` (copy `.env.example`). Adjust
`SONARR_SERVER` and `TZ` in the compose file, then:

```bash
cp .env.example .env      # fill in SONARR_TOKEN and SONARR_AUTH_TOKEN
docker compose up -d
```

`make docker` builds the same image from source, tagged `sonarr-mcp`, with version info from
git. The image is alpine-based (so `docker exec -it sonarr-mcp sh` works), runs as a non-root
user and has a healthcheck against `/healthz`. The binary is the entrypoint, so `docker run --rm
ghcr.io/katbyte/sonarr-mcp info` works as a connectivity check with the `SONARR_*` variables
passed via `-e`.

## MCP Tools

Tools are named resource-first (`series_*`, `episode_*`, `queue_*`, `audit_*`...) so they group
by what they act on. Every tool carries MCP annotations (read-only or destructive) and tools
that change Sonarr say so in their descriptions. Wherever a tool takes a series it accepts a
title, a title with its year (`Chernobyl (2019)`), `tvdb:<id>` or Sonarr's id, and a name that
matches several series comes back as an error listing them; episodes are `S01E02`, `1x02` or
ids, and quality profiles, tags, indexers, download clients and root folders are taken by
name.

| Resource | Tools |
|---|---|
| server | `server_info`, `server_health`, `task_list`, `task_run`, `command_list` (what Sonarr is doing and just did), `log_list` |
| series | `series_list` (filter by title, monitoring, status, tag, profile, or only those missing episodes), `series_get`, `series_lookup` (TheTVDB, through Sonarr), `series_add`, `series_import` (folders already on disk, added where they are), `series_edit` (monitoring, profile, type, season folders, new seasons, folder with or without moving the files, tags), `series_refresh`, `series_rescan`, `series_rename` (with `dry_run`), `series_search`, `series_delete` |
| seasons and episodes | `season_monitor`, `season_search` (season packs first), `episode_list`, `episode_monitor`, `episode_search`, `calendar_list` |
| files | `file_list` (quality, cutoff, languages, release group, custom formats and score, and what the streams say), `file_edit` (quality, languages, release group), `file_delete`, `import_scan` (what Sonarr makes of a folder of files), `import_apply` |
| searching | `release_search` (every release, with Sonarr's reasons for rejecting each), `release_grab` (any of them, rejected or not), `release_parse` (what Sonarr reads from a name), `wanted_search` (everything missing, or everything below cutoff) |
| downloads | `queue_list`, `queue_grab` (send what a delay profile holds), `queue_remove` (from the client too, optionally blocklisting), `history_list`, `history_mark_failed`, `blocklist_list`, `blocklist_remove` |
| settings | `profile_list`, `customformat_list`, `tag_list` (and what uses each tag), `tag_create`, `tag_delete`, `config_get` (naming, media management, host, UI, indexer, download client and import list options), `rootfolder_list`, `rootfolder_add`, `rootfolder_remove` |
| providers | `indexer_list`, `indexer_test`, `downloadclient_list`, `downloadclient_test` (as Sonarr's Test button, saying what is wrong with each that fails; credentials never shown) |
| audits | the 16 audits and `audit_all` in [the table above](#the-audits) |

`series_delete` (which removes a series, and its files if asked) and `file_delete` (which
deletes files from disk) are only registered when `--enable-delete` / `SONARR_ENABLE_DELETE` is
set. `--read-only` registers the 43 read tools and
nothing else, so a write tool is absent from `tools/list` rather than refused when called.

### Choosing which tools load

**The default is `core`: six read-only tools, about 850 tokens.** The whole surface is around
9,200 tokens of tool definitions before anyone asks a question, which is a poor way to spend a
client's context by default. `--toolsets` / `SONARR_TOOLSETS` loads the groups a session
actually needs, and `core` comes along with whatever else is asked for, because nothing else
can find a series.

**Running the library day to day needs `SONARR_TOOLSETS=curation`** - every audit, and everything
that fixes what they find. Adding shows is `library`; settings, providers, logs and the delete
tools are `admin`. `SONARR_TOOLSETS=all` restores every tool.

| toolset | tools | with core | ~tokens |
|---|---|---|---|
| `core` *(default)* | 6 | 6 | 850 |
| `library` | 5 | 11 | 1,500 |
| `admin` | 11 (13 with `--enable-delete`) | 17 (19) | 1,700 (1,950) |
| `curation` | 45 | 51 | 7,700 |
| `all` | 67 (69 with `--enable-delete`) | 67 (69) | 9,200 (9,400) |

Tokens are what the model sees: each tool's name, description and input schema, measured over
a real `tools/list` at four bytes a token. Every tool also carries an output schema, another
15,500 tokens across `all`, but clients keep that to themselves to validate results rather than
sending it to the model.

`--toolsets` also takes a resource family - `series`, `episode`, `season`, `file`, `queue`,
`history`, `release`, `audit`, `tag`, `profile` and the rest `sonarr-mcp tools` lists - which is
every tool with that prefix:

```sh
SONARR_TOOLSETS=all                 # every tool
SONARR_TOOLSETS=curation            # audits plus everything that fixes what they find
SONARR_TOOLSETS=audit               # read-only detection, nothing that writes
SONARR_TOOLSETS=core,queue,history  # core plus two whole families
```

`sonarr-mcp tools` prints what the current flags would register, grouped by toolset, and needs
no server:

```sh
sonarr-mcp tools                    # the default set
sonarr-mcp tools --toolsets all     # every tool
sonarr-mcp tools --read-only -q     # names only
```

### Narrowing further

`--allow-tools` and `--deny-tools` narrow whatever the toolsets left, and take comma-separated
tool names, globs with a leading or trailing `*`, or the `essential` preset (`series_list`,
`series_get`, `calendar_list`, `queue_list`, `series_search`):

```sh
SONARR_ALLOW_TOOLS=essential
SONARR_ALLOW_TOOLS=series_*,episode_list,queue_list
SONARR_DENY_TOOLS=*_delete,*_search
```

A pattern that matches no tool aborts startup and names it, so a typo cannot silently hide a
tool.

### A typical session

1. `audit_all` says where the library needs work.
2. `audit_missing_episodes` lists what aired and never arrived; `episode_search`,
   `season_search` or `wanted_search` go and get it, and report what reached the queue.
3. `audit_stuck_downloads` lists what is stuck and Sonarr's reason. A download Sonarr could
   not match is imported with `import_apply`, naming the series and episodes; a bad release
   is removed with `queue_remove blocklist=true`, and Sonarr searches for another.
4. `audit_untracked_files` and `audit_unmapped_folders` find what is on disk and not in
   Sonarr; `import_apply` and `series_import` take it in where it is.
   `audit_missing_files` and `audit_missing_folders` find the reverse - records with
   nothing behind them - and say whether a folder simply moved, which `series_edit path`
   and `series_rescan` put right, or is gone, which `series_rescan` clears.
5. `audit_quality_mismatch` finds files recorded at the wrong quality; `file_edit` corrects
   them, so Sonarr upgrades what it should and stops chasing what it should not.
6. `audit_naming` shows what does not match the naming format; `series_rename dry_run=true`
   shows the renames, and `series_rename` makes them.
7. `audit_monitoring`, `audit_series_settings` and `audit_profiles` find the settings that
   will quietly miss episodes or do nothing; `series_edit`, `season_monitor` and
   `tag_delete` fix them.

## Using the client on its own

`lib/sonarr` is a complete Go client for Sonarr's v3 API that depends on nothing but the
standard library and the shared base client in `lib/client`, and knows nothing of MCP. If you
only want to talk to Sonarr from Go, take the package and ignore the rest:

```go
import "github.com/katbyte/sonarr-mcp/lib/sonarr"

c, err := sonarr.New("http://nas:8989", os.Getenv("SONARR_TOKEN"))
series, err := c.GetSeries(ctx, sonarr.GetSeriesOperationOptions{})
for _, s := range series.Model { ... }

missing, err := c.GetWantedMissingComplete(ctx, sonarr.GetWantedMissingOperationOptions{
    Monitored: new(true), IncludeSeries: new(true),
}) // every page
for _, e := range missing.Items { ... }
```

It is generated from Sonarr's own OpenAPI document (`docs/`, see
[docs/README.md](docs/README.md)) by `internal/pandorest`, a generator kept in this
repository and modelled on [hashicorp/pandora](https://github.com/hashicorp/pandora): an
importer normalises the spec into checked-in definitions (`api-definitions/`, one file per
tag) through named workarounds for the spec's known bugs, a differ reports what a spec
refresh changes, and a generator writes one file per operation and model from the
definitions. That is **a method for every one of Sonarr's 228 operations**, each with typed
options, a typed body, a `{Model, HttpResponse}` result, the status codes the operation
answers with (anything else is an error), and a `Complete` pager on every paged list. `make
apicheck` proves the coverage claim against the spec, `make gencheck` (and the unit tests)
fail when the generated code is stale, and the integration suite proves the shapes **against
a running Sonarr** - which is the only thing that catches the server answering differently
from what its spec says. See [internal/pandorest/README.md](internal/pandorest/README.md).

## Development

```bash
make            # fmt + build
make check-all  # build + unit tests + both live suites (needs docker) + every linter
```

### Tests

`make test` is hermetic and fast. It covers the pure logic - tool registration and toolsets,
the audit heuristics, the CLI's flags, config files and HTTP auth, the record/replay proxy, the
fake indexer and download client - and, against a canned server, the requests the base client
and the generated client build and the answers they decode. It also re-imports the spec and
regenerates the SDK to check the checked-in code is current, and applies every importer
workaround twice to prove each one notices when its bug is fixed.

Everything else runs against **a real Sonarr in Docker**, because a stub can only confirm what
you already believed. Two suites, each in its own container:

| | Covers | Command |
|---|---|---|
| `integration/` | the `lib/sonarr` client: every one of its operations is called against the server - all but the four that restart, stop or restore Sonarr, which are named with the reason - checking that each request is one Sonarr accepts and each answer decodes with its fields populated, and a run that leaves an operation neither called nor explained fails | `make testacc-integration` |
| `acceptance/` | the tools: name resolution, projections, every audit against a library seeded with what it exists to find, searches that grab from the fake indexer and downloads the fake SABnzbd completes, imports, renames, and journeys that chain them (an audit's finding, the fix it names, the audit again to see it gone), and the built binary itself over stdio and HTTP (flags and environment reaching the server, the bearer check, refusing to start without a key or a token) | `make testacc-acceptance` |

```bash
make testacc              # both suites, each in a throwaway container
make testacc-acceptance   # one suite
make check-all            # build + unit + live suites + every linter
make cover                # every suite merged into one coverage number
```

Coverage has to span every suite or it lies: `go test -cover ./...` reports a fraction for
`tools/`, because almost everything real happens in the live suites behind the `integration`
tag. `make cover` runs each into its own binary coverage directory and merges them with
`go tool covdata` - stdlib tooling, no third-party merger - which is what the badge reports.
The generated `lib/sonarr` is left out of the number and reported on a line of its own: it is
one mechanical method per operation, and what proves it is that the integration suite calls
224 of its 228 operations against a real Sonarr, not a line count.

**Every tool is exercised, and every audit's findings with it.** Coverage is enforced rather
than claimed: the acceptance suite records every tool it calls and fails if the server
registered one nothing called, so a new tool cannot ship untested. The same goes for what the
audits report - each audit declares the kinds of finding it can make, and a run fails if the
seeded library never produces one of them and nothing says why. The few that a real Sonarr
cannot be made to produce (a download stuck for a day, a disk nearly full, a folder the
scan will not account for) are listed with their reason and the unit test that covers them
instead. Nothing in either suite talks to a real indexer or download
client: a fake Newznab indexer and a fake SABnzbd (`internal/fakes`) run inside the test
process, serve releases for the fixture series and write a finished download's files when the
test says so. Sonarr's own calls out to TheTVDB (through SkyHook), services.sonarr.tv, XEM and
the artwork CDN go through a record/replay proxy (`lib/providerproxy`) - the container is
started with `HTTPS_PROXY` pointing at it and trusts its certificate authority - so neither
suite needs a network:

```bash
make record         # re-record the cassettes against the real providers
make record-check   # check the cassettes still match, without rewriting them
```

`record-check` compares the *shape* of live responses against the recordings - renamed fields,
vanished fields, changed types - and ignores values, so it goes red when a provider changes its
contract rather than when an air date moves. The recordings keep only the suite's own series
out of the lists of every show a provider knows (the scene names alone are 1.6 MB).

Fixtures are generated, never committed: `scripts/testenv.sh` writes tiny videos with `ffmpeg`
(black frames and a silent audio track, about half a megabyte for a 45 minute episode, so
Sonarr reads a real resolution, codec, runtime and language from each) under
`~/.cache/sonarr-mcp` (`SONARR_TEST_DATA` to move them - not `$TMPDIR`, which a Docker VM does
not share by default), writes Sonarr's config with a known API key, and prints the environment.
The suites add the root folder, import and add the series, and set up the indexer and download
client through the tools, so building the fixtures is itself part of the coverage. The library
is seeded with every defect the audits exist to find - a gap in a season, a file deleted behind
Sonarr's back, a file copied in by hand, a folder Sonarr does not know, a 360p file named
1080p, five minutes of video standing in for a 45 minute episode, a German dub, an anime typed
standard, a continuing series ignoring new seasons, a tag nothing uses - and each audit has a
test against it.
`scripts/testenv.sh fixtures` writes just the media tree if you want to look at the layout.
Requires docker (or Colima), ffmpeg, jq and openssl; the suites skip when `SONARR_SERVER` and
`SONARR_TOKEN` are unset, so they never fail for want of a daemon.

Dev tools are pinned in `.tools/go.mod` (actionlint in `.tools/actionlint/go.mod`) and built
into `.tools/bin` by make. On a noexec checkout point `TOOLS_BIN` somewhere local, e.g.
`make TOOLS_BIN=~/.cache/sonarr-mcp/bin lint`.
