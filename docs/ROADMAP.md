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
7. **Every audit names its fix.** A finding says which tool fixes it, and the acceptance
   suite runs that fix and the audit again.

## Done

| Area | Tools | Answers |
|---|---|---|
| know the library | `server_info`, `series_list`, `series_get`, `episode_list`, `calendar_list`, `file_list`, `profile_list`, `customformat_list`, `tag_list`, `rootfolder_list`, `config_get` | "what do I have, and how is it set up" |
| curation | `audit_all` + 15 audits, `release_parse`, `file_edit`, `series_edit`, `season_monitor`, `episode_monitor`, `series_rename`, `series_rescan`, `series_refresh`, `import_scan` → `import_apply`, `series_import`, `tag_create`, `tag_delete` | "what is wrong, and fix it" |
| getting episodes | `episode_search`, `season_search`, `series_search`, `wanted_search`, `release_search` → `release_grab`, `queue_list`, `queue_grab`, `queue_remove`, `history_list`, `history_mark_failed`, `blocklist_list`, `blocklist_remove` | "go and get what is missing, and see it arrive" |
| growing the library | `series_lookup`, `series_add`, `rootfolder_add`, `rootfolder_remove` | "add this show" |
| admin | `server_health`, `task_list`, `task_run`, `command_list`, `log_list`, `indexer_list`, `indexer_test`, `downloadclient_list`, `downloadclient_test`, `series_delete`, `file_delete` | "keep it healthy" |

## Candidates

| Tool | Endpoints | Answers |
|---|---|---|
| `profile_edit` | `PUT /api/v3/qualityprofile/{id}` | change a profile's cutoff, allowed qualities and format scores (the audit finds unused profiles; nothing yet edits a used one) |
| `customformat_create` / `customformat_edit` | `/api/v3/customformat` | the formats a profile scores, e.g. preferring x265 or an anime group |
| `series_bulk_edit` | `PUT /api/v3/series/editor` | one change across many series: a profile, a root folder, tags (today it is one `series_edit` per series) |
| `importlist_list` | `/api/v3/importlist`, `/api/v3/importlistexclusion/paged` | what adds series on its own, and what it has been told never to add |
| `episode_history` | `GET /api/v3/history/series` | one series' or episode's history without paging the whole library's |

## Guarded / deliberately excluded

- `series_delete` (optionally with its files) and `file_delete`: only registered when
  `--enable-delete` (`SONARR_ENABLE_DELETE`) is set.
- Not wrapping **as tools**, ever: authentication and the API key, updates and backups
  (restore especially), the host settings (port, SSL, URL base, proxy), notifications and
  their plumbing, the UI settings, and the system shutdown and restart. They are one-off
  administration better done in Sonarr's own web interface, and some would cut the
  server's own connection.
- Adding or editing indexers and download clients: they carry credentials, and a model has
  no judgment to add to typing them in. Listing and testing them is wrapped, because that is
  where a stuck library is diagnosed.
