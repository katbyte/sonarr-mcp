# Tool roadmap

Design rules, in priority order:

1. **Wrap judgment, not plumbing.** A tool exists only where a model has a decision to
   make. Backups, updates, the UI settings and notification plumbing stay unwrapped.
2. **Trim every response.** Tools return the fields a decision needs, never raw resources
   (Sonarr's series resource is 45 fields with the seasons, statistics and images nested
   inside; `series_list` rows carry what a list needs).
3. **Composite over chatty.** If a task always takes N calls (search → wait for the grab →
   check the queue), it is one tool, not N.
4. **Names, not just ids.** Every tool that takes a series, quality profile, tag, indexer,
   download client or root folder resolves a name, and one that fits several says which.
5. **Resource-first names** (`series_*`, `episode_*`, `queue_*`, `audit_*`) so tools group
   by what they act on.
6. **Reads are cheap, writes are explicit, destructive is opt-in.** Every tool carries MCP
   annotations; anything that changes Sonarr says so in its description; anything that
   deletes series or files is disabled unless the operator sets `--enable-delete`.
7. **Every audit names its fix, and every kind of finding is tested.** A finding says which
   tool fixes it; the live suite has to report every kind of finding an audit can make, or
   name the reason it cannot (`acceptance/tools/coverage_test.go`), and where the fix is a tool it
   runs the fix and the audit again.
8. **One way to page.** Every list takes `limit` and `offset` and answers `total`, `offset` and `next_offset`, whatever it lists and whether Sonarr pages it or the tool does. A test holds every `_list` tool to it.
9. **`confirm` before what cannot be put back.** A tool that deletes a series or files changes nothing without `confirm`, and says what it would do: `would_delete`. The same test holds every delete tool to it.
10. **Nothing hidden in an answer.** Text that came from outside - a release's name, a title, a message - is cleaned of what does not show and cut to a length, once, around every tool, so a new tool cannot forget to. What a later call hands back (a path, a guid, a download's id) is left as Sonarr holds it, and a test holds that list to fields the answers really have.

## Done

| Area | Tools | Answers |
|---|---|---|
| know the library | `server_info`, `series_list`, `series_get`, `episode_list`, `calendar_list`, `file_list`, `profile_list`, `customformat_list`, `tag_list`, `rootfolder_list`, `config_get` | "what do I have, and how is it set up" |
| curation | `audit_all` + 17 audits, `release_parse`, `file_edit`, `series_edit`, `series_bulk_edit`, `season_monitor`, `episode_monitor`, `series_rename`, `series_rescan`, `series_refresh`, `import_scan` → `import_apply`, `series_import`, `tag_create`, `tag_delete` | "what is wrong, and fix it" |
| getting episodes | `episode_search`, `season_search`, `series_search`, `wanted_search`, `release_search` → `release_grab`, `queue_list`, `queue_grab`, `queue_remove`, `history_list`, `history_mark_failed`, `blocklist_list`, `blocklist_remove` | "go and get what is missing, and see it arrive" |
| growing the library | `series_lookup`, `series_add`, `rootfolder_add`, `rootfolder_remove` | "add this show" |
| admin | `server_health`, `server_disk_space`, `server_updates`, `server_restart`, `server_shutdown`, `task_list`, `task_run`, `command_list`, `command_cancel`, `log_list`, `logfile_list`, `logfile_read`, `filesystem_list`, `backup_list`, `backup_create`, `backup_restore`, `backup_delete`, `indexer_list`, `indexer_test`, `downloadclient_list`, `downloadclient_test`, `series_delete`, `file_delete` | "keep it healthy" |
| settings | `config_get`, `config_edit`; add, edit and delete for indexers, download clients, notifications, import lists, delay and release profiles, remote path mappings, custom formats, auto tags and saved filters, with `_kinds` and `_test` for the providers; `exclusion_list`, `exclusion_add`, `exclusion_remove`; `metadata_list`, `metadata_edit`; `profile_create`, `profile_edit`, `profile_delete`; `qualitydefinition_list`, `qualitydefinition_edit`; `language_list`; `tag_rename`; `config_naming_preview`; `season_bulk_monitor` | "set it up" |

## Candidates

| Tool | Endpoints | Answers |
|---|---|---|
| a torrent client fake | - | the live suite's torrent path runs through Sonarr's torrent blackhole, which needs no client; a qBittorrent or Transmission stand-in in go-kt would exercise the queue for a torrent as SABnzbd's does for usenet |

## Guarded

- **Deleting is opt-in, and waits to be confirmed.** Every `_delete` tool - a series, episode files, an indexer with its credentials, a profile, a backup - is only registered when `--enable-delete` (`SONARR_ENABLE_DELETE`) is set, and changes nothing without `confirm`.
- **Four writes wait to be confirmed too:** a change to the host's settings (the port, the address, signing in), which can cut this server off from Sonarr; `backup_restore`, which puts everything Sonarr knows back to how it was; `server_restart`; and `server_shutdown`, after which nothing here can start it again.
- **Credentials are set and never shown.** An indexer's API key, a download client's password, a notification's token go in through `settings` and are left out of every answer: whatever Sonarr marks private, whatever is named as a credential is (a key, a token, a secret, a cookie, a password), a webhook's own headers, and an address that is itself the credential (a Discord or Slack webhook's, Apprise's), since Sonarr does not mark them all. A live test holds every setting of every kind of provider to hidden or looked at. An address is shown without who it signs in as or the value of any parameter, in a setting and wherever Sonarr quotes one back, which it does with an indexer's stored key on the end. The log line for the write names each setting and blanks its value. The live suite reads every answer and refusal it gets for the keys it set, and fails on one.
- **Sonarr tries a provider before it saves one.** An indexer, client, notification or import list that fails its test is refused unless `force` is set.

## Not wrapped, and why

- Signing in and out of the web interface (`/login`, `/logout`): this server is given an API key, and has no session to open.
- The calendar feed (`/feed/v3/calendar/sonarr.ics`): a link for a calendar application, which carries the API key in its address; `calendar_list` answers the same episodes.
- The web interface's own plumbing: its translations (`/localization`), its static files and the routes dump.
- Restoring a backup from an uploaded file: `backup_restore` restores one Sonarr already holds, and a model has no file to hand over.
- Language profiles: Sonarr 4 keeps the endpoints for old clients and no longer uses them.
- Sonarr's bulk forms of what a tool already does one at a time: editing or deleting several indexers, download clients or import lists in one call, and deleting several series at once. Several calls do the same, each answering for itself, and a delete of many series in one call is the one mistake that is hardest to take back.
- What its settings pages ask a provider while a form is being filled in (the `action` endpoints: a device list, an OAuth start): they answer a form, not a caller.
- Reordering delay profiles, and the lists of indexer flags and of the limits a quality's size may be set within.

A test holds this list to the code: every operation the Go client has is called by some tool, or is named in `notWrapped` (tools/tools_test.go) with its reason, and an operation a tool comes to call is reported there as a reason gone stale.
