# Changelog

## Unreleased

### Breaking

- `tag_delete` is a delete tool like the rest: it is only registered with `--enable-delete`, and changes nothing without `confirm: true`
- `--enable-delete` registers every tool that deletes, where it registered the two that delete series and files: the `_delete` tool of each kind of settings record comes with it
- `config_get` and `customformat_list` are in the new `settings` toolset, where they were in `admin`
- `series_delete` and `file_delete` change nothing without `confirm: true`: called without it they answer `would_delete`, the series or the files a confirmed call would remove, and with `delete_files` what would go from disk
- every list is paged one way: every `_list` tool takes `limit` (default 100, at most 1000) and `offset`, and answers `total`, `offset` and `next_offset`. The ten that took no limit now return a page; `episode_list` returns 100 by default where it returned 300, and `history_list` and `log_list` 100 where they returned 50; `log_list`'s `total_at_level` is `total`
- the Go client moves from `lib/sonarr` to `sdk/sonarr`, as in embyfin-mcp and abs-mcp: change the import path
- the Go client's base client is pandorest's, `github.com/katbyte/pandorest/client`, in place of `lib/client`: its error for a refused call is `*client.StatusError` there, with `Expected` where `ExpectedStatusCodes` was, and `HttpMethod` is `HTTPMethod`
- the Go client refuses a redirect rather than following it, since following one turns a write into a read that reports success: point it at the address Sonarr itself answers on
- in the Go client a command read back keeps its own fields: `CommandResource.Body` is a `map[string]any` holding whatever that command carries (`seriesId`, `files`, `importMode`...), where it was a `*Command` that dropped them
- in the Go client a number among an operation's options is a pointer, as a flag already was, so a 0 can be asked for: `Page: 2` is `Page: new(2)`, and season 0, the specials, can be named
- `--allow-tools` beside `--toolsets` adds to them, "these sets and these tools as well", where it kept only the tools both held; `--deny-tools` narrows. On its own it is only the tools it names, where the default `core` set was mixed in and `--allow-tools essential` loaded four of its five tools

### Added

- **settings can be changed, not only read**: `config_edit` changes a section of Sonarr's settings by the names `config_get` shows (naming, media management, indexer, download client and import list options, the web interface, the host), says what changed from and to, and says what Sonarr accepted and did not keep; the host's settings wait for `confirm`, since they can cut the server off
- **every kind of record Sonarr keeps is added, changed and deleted the one way**: indexers, download clients, notifications and import lists (each with `_kinds`, the kinds there are and the settings each takes, and `_add`, `_edit`, `_delete`), `notification_list` and `notification_test`, `importlist_list` and `importlist_test`, `metadata_list` and `metadata_edit`, and `_list`, `_add`, `_edit`, `_delete` for delay profiles, release profiles, remote path mappings, auto tags and the web interface's saved filters; `customformat_add`, `customformat_edit`, `customformat_delete`. Credentials are set and never shown, those Sonarr marks private and those only named as one (the key an import list keeps for another Sonarr is not marked), and Sonarr tries a provider before saving it unless `force` is set. Sonarr's own force only does that for one being changed, so one added with `force` goes in switched off, which Sonarr does not try, and is then switched on; and when Sonarr's test of every notification or import list gives no answer because one of them cannot be reached at all, each is tried on its own and the one at fault is named
- `profile_create`, `profile_edit` and `profile_delete`: the qualities a profile allows, where it stops upgrading, and the score it gives each custom format; `qualitydefinition_list` and `qualitydefinition_edit` for the sizes a release of each quality may be
- `series_bulk_edit` makes one change across many series; `exclusion_list`, `exclusion_add` and `exclusion_remove` for what import lists may not add; `tag_rename`; `language_list`
- running Sonarr itself: `backup_list`, `backup_create`, `backup_restore` and `backup_delete`; `server_updates`, `server_disk_space`, `server_restart` and `server_shutdown`; `logfile_list` and `logfile_read`, for the log files on disk; `command_cancel`; `filesystem_list`, for folders as Sonarr sees them
- `audit_server`: what is wrong with Sonarr itself rather than the library - no backup or none for over a week, a release newer than the one running, and the last day's errors in the log by what logged them
- `config_naming_preview` shows what a naming format would name files and folders before `config_edit` sets it; `season_bulk_monitor` monitors or unmonitors the same seasons across many series in one call, as Sonarr's Season Pass page does; `queue_grab` sends every id in one call
- the live suite has the one journey every server in the family has: a setup built from nothing with the tools alone, audited, its own mistakes fixed with the tools the findings name, taken down again, and Sonarr compared to how it was found
- a test holds every operation of the Go client to a tool that calls it or a listed reason it is not wrapped, so the roadmap's list of what is not wrapped cannot drift from the code
- a `settings` toolset holding the tools that set Sonarr up
- text that came from outside Sonarr - a release's name, a title or plot from TheTVDB, a download client's message, a line of the log - has what does not show taken out of it and is cut at 2000 characters, in every answer and every refusal, so nothing can be hidden in a name; a path, a folder, a `guid` and a download's id are left as Sonarr holds them
- a client that connects is told the rules that hold between the tools it was given: to treat answered text as data, how a series is named, how lists page, that a delete waits for `confirm`, and to start at `audit_all`
- with `SONARR_LOG=info`, every call of a write or delete tool is logged as one line: the tool, its arguments, and whether it was refused, failed or answered
- a call with an argument a tool does not take is told the arguments it does take, where it was only told which one was not expected
- `file_delete` names every episode each file holds, since a file of two episodes takes both, with its size and whether Sonarr still monitors them, under `files`; and it warns, before deleting and after, when those episodes are still monitored: that Sonarr downloads them again, or, with its Unmonitor Deleted Episodes setting on, that it stops monitoring them
- `command_list` takes a `command_id`, so a search, refresh or rename still running when its wait ended is followed by the id its tool answered, rather than looked for among the housekeeping Sonarr runs every minute
- `SONARR_LOG=trace` shows every request and answer with every credential blanked: Sonarr's own API key, the host's password and its confirmation, and each setting's value of an indexer, download client, notification or import list, since those travel as name and value pairs and a key is the value of a setting named apiKey
- `sonarr.New` takes the base client's options: a logger with `client.WithLog`, a transport with `client.WithTransport`, a retry rule with `client.WithRetry`
- the Go client says which Sonarr release its API document was taken from, `sonarr.DocumentVersion`, beside the version the document gives itself

### Changed

- the Go client asks for what a call is documented to answer: the five calls that fetch a file (a log file, an update log, a cover, the routes, the calendar feed) ask for that type first, where every call asked for JSON alone. This comes with pandorest v0.6.0, which also brings ready-made forms of the fixes for Sonarr's API document; the ones kept here are unchanged for now
- an audit reads eight series at a time, where it read them one after another: over three thousand series, the reads that took twelve seconds and more take two. What it finds, and the order it finds it in, is the same
- `audit_all` keeps the counts of the audits that ran when one could not: that audit is named under `failed`, its row carries the `error` and no `findings`, so a count that is unknown is not read as none. One failing audit used to fail the whole call
- `season_monitor` and `season_search` name the seasons a series has when asked for one it does not
- the live suite now fails when Sonarr sends a field the Go client has no place for, where it only caught a whole object lost; the one case it found is the command body above
- a read is asked for again when a gateway could not reach Sonarr or the connection dropped, three times in all; a write is still sent once
- Sonarr's log shows which sonarr-mcp is asking: the user agent carries the version
- the Go client is generated by [pandorest](https://github.com/katbyte/pandorest), the generator the katbyte MCP servers now share, in place of the copy kept here; Sonarr's API document and its definitions are under `api-defs/`, named by the release they came from
- every write tool tells a client whether it can change or take away what was there, and whether calling it again changes anything more, where all of them said they were not destructive: a search, a grab and an import can replace the file an episode had; adding a tag, a root folder or a series only adds
- a tool that fails unexpectedly answers its call with an error naming the tool, and the session carries on
- over HTTP, a session its client stopped using is closed after half an hour, and stopping the server no longer waits out a connected client
- serving, the tool registry and the live suites' recording proxy are go-kt's, shared with embyfin-mcp, abs-mcp and technitium-mcp
- the Docker image and the release build take their base images from Google's mirror of Docker Hub, and from Docker Hub itself when the mirror gives nothing
- every test that runs against a real Sonarr is under `acceptance/`, behind the `acceptance` build tag, where there were two folders behind `integration`: the Go client's suite is `acceptance/sonarr` (`make testacc-sdk`, was `integration/` and `make testacc-integration`) and the tools' is `acceptance/tools` (`make testacc-tools`, was `make testacc-acceptance`). The lint reads both, which it never had
- the helper scripts are in `.scripts/`, where they were in `scripts/`: the test environment is `.scripts/testenv.sh`

### Fixed

- `series_add` answered before Sonarr had read the show's episodes, and Sonarr sets what the add asked to be monitored only then, over anything changed in between: a season monitored straight after adding was unmonitored again a moment later. `series_add` now waits for Sonarr to finish (`wait_seconds`, default 60) and answers `ready`
- a webhook's own headers, where its token goes, were shown by `notification_list` and by what an add or an edit answers, since Sonarr does not mark them private; so were a Discord or Slack webhook's address and Apprise's addresses, each of which is its own credential. All are set and never shown. A live test now reads every setting of every kind of provider Sonarr has (437 over 64 kinds) and fails on one that is shown as text and has not been looked at
- `indexer_test` handed a caller the API key Sonarr keeps for an indexer: when the indexer answers with an error, Sonarr says where it failed, the address it asked with the key on the end, and that was passed on as a problem. Every address in an answer or a refusal is now shown without who it signs in as and without the value of any parameter
- a search asked to queue and return at once (`wait_seconds: -1` on `episode_search`, `season_search`, `series_search` and `wanted_search`) waited all the same, for as long as Sonarr was busy and up to thirty seconds: the check of the download clients a search is followed by is a command of its own, and it waited its turn behind the search. Such a search now answers at once, with the queue as it stands
- `command_list` answered Sonarr's oldest commands first, so on a busy server the first page held none of what was running or had just finished: what is running or waiting comes first, then what finished, newest first
- `audit_cutoff_unmet` told a profile with upgrades off to search for the files below cutoff, which brings nothing in: Sonarr refuses every release for an episode that has a file while the profile does not upgrade. The finding now says so, and names `profile_edit` to turn upgrades on first
- an audit whose fix is a change to Sonarr's settings names the tool that makes it (`config_edit`, `profile_edit`, `profile_delete`, `customformat_delete`, `releaseprofile_edit`), where it sent a person to Sonarr's own pages
- `season_monitor` asked to unmonitor a season that already said it was unmonitored did nothing, though episodes in it were still monitored and Sonarr went on searching for them: a season's flag is its own, and `episode_monitor` leaves it be. It now sets the episodes the flag missed, and says how many
- `season_monitor` asked to monitor a season already monitored, in a series that is not, did nothing: it now monitors the series, as it did when the season's flag changed
- edits of one series made in the same turn no longer undo each other: `series_edit` and `season_monitor` each sent the whole record back as they found it, so of eight calls each adding a tag two tags stayed, and a season turned off beside them came back on; they now take turns at a series, and a tag several calls add is made once

## 0.1.0 (2026-09-19)

The first release: an MCP server, CLI and Go SDK for running and auditing a Sonarr library.

- **16 audits and `audit_all`**, each a sweep over the whole library for one thing that goes wrong, returning a worklist that names the tool that fixes it: episodes that aired and never arrived, files below their profile's cutoff, downloads stuck or failing, files and folders Sonarr has lost track of or never knew, a folder renamed or moved out from under it, names off the format, files recorded at the wrong quality or running the wrong length or in the wrong language, monitoring that will miss new seasons, series set up wrong, settings that do nothing, and Sonarr's own health checks.
- **69 tools in toolsets**: series, seasons, episodes and files; searching, grabbing and the queue; history and the blocklist; imports, renames, rescans and refreshes; tags, profiles, custom formats and root folders; indexers and download clients, listed and tested; tasks, commands and logs. The default `core` set is six read-only tools, about 850 tokens; `curation` loads the audits and everything that fixes what they find.
- **Deleting is opt-in.** `series_delete` and `file_delete` are only registered with `--enable-delete`, and `--read-only` registers the 43 read tools and nothing else.
- **`lib/sonarr`**: a complete Go client for Sonarr's v3 API, all 228 operations, generated from Sonarr's own OpenAPI document with the document's bugs fixed by named workarounds, each of which fails the import once Sonarr fixes its bug. The tests call 224 of the 228 against a real Sonarr; the other four restart, stop or restore it.
- **Tested against a real Sonarr 4.0.20** in Docker, with a fake indexer and a fake SABnzbd the tests drive, so searches really grab and downloads really import. The suite fails if a registered tool has no test, or if an audit can report a kind of finding the seeded library never produced and nothing explains. Sonarr's calls to TheTVDB and its own services are recorded and replayed, so the tests need no network.
- Serves MCP over stdio, or over HTTP with a bearer token (`--listen`), and ships as a container image.
