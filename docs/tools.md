# Tools

Every tool sonarr-mcp has, and the rules they all follow. `sonarr-mcp tools --toolsets all --enable-delete` prints the same list from the program itself.

Which tools load is in [toolsets.md](toolsets.md). The audits have a page of their own: [audits.md](audits.md).

## Rules every tool follows

- **Names.** A tool is named for what it acts on, then what it does: `series_list`, `queue_remove`, `audit_naming`.
- **Naming a series.** Give a title, a title with its year (`Chernobyl (2019)`), `tvdb:<id>` or Sonarr's id. A name that fits several series is refused, and the answer lists them.
- **Naming other things.** An episode is `S01E02`, `1x02` or its id. Profiles, tags, indexers, download clients and root folders go by name.
- **Lists page the same way.** Each takes `limit` (100 unless given, 1000 at most) and `offset`. Each answers `total`, `offset` and, while there is more, the `next_offset` to pass back.
- **Tools say what they do.** Each is marked for the client as read-only or as able to destroy something, and one that changes Sonarr says so in its description.
- **A wrong argument is refused.** The answer names the arguments the tool does take.

## Guard rails

- **Read-only means absent.** `--read-only` loads the 69 tools that change nothing. A tool that changes Sonarr is then not listed at all, rather than refused when called.
- **Deleting is opt-in.** The 15 delete tools load only with `--enable-delete`. They are `series_delete`, `file_delete`, `tag_delete`, `backup_delete`, and the `_delete` tool of each kind of settings record.
- **A delete waits to be confirmed.** Without `confirm` it changes nothing and answers `would_delete`: what a confirmed call would remove.
- **`file_delete` warns first.** It names every episode each file holds. It warns when Sonarr still monitors them, since Sonarr then downloads them again, or stops monitoring them if it is set to.
- **Four more wait for `confirm`.** They are `server_restart`, `server_shutdown`, `backup_restore`, and a change to the host's settings, which can cut this server off from Sonarr.

## Series

| Tool | What it does |
|---|---|
| `series_list` | Lists series. Filters by title, monitoring, status, tag, profile, or only those missing episodes. |
| `series_get` | One series in full, with each season's monitoring and counts. |
| `series_lookup` | Searches TheTVDB, through Sonarr, for a show to add. |
| `series_add` | Adds a show, into a root folder and a quality profile. |
| `series_import` | Adds folders already on disk as series, where they are. |
| `series_edit` | Changes monitoring, profile, type, season folders, new seasons, tags, or the folder, with or without moving the files. |
| `series_bulk_edit` | Makes one change across many series. |
| `series_refresh` | Reads a series again from TheTVDB, then rescans its folder. |
| `series_rescan` | Picks up files added, removed or replaced on disk. |
| `series_search` | Searches for every missing or below-cutoff episode of a series. |
| `series_rename` | Renames a series' files to the naming format. |
| `series_delete` | Removes a series, and its files if asked. |

## Seasons and episodes

| Tool | What it does |
|---|---|
| `season_monitor` | Monitors or unmonitors whole seasons of a series. |
| `season_bulk_monitor` | Does the same seasons across many series, as Sonarr's Season Pass does. |
| `season_search` | Searches for a season, season packs first. |
| `episode_list` | A series' episodes, with air dates and what is on disk. |
| `episode_monitor` | Monitors or unmonitors episodes. |
| `episode_search` | Searches for particular episodes and grabs the best release. |
| `calendar_list` | What airs in the next days. |

## Files and importing

| Tool | What it does |
|---|---|
| `file_list` | A series' files: quality, cutoff, languages, release group, custom formats and score, and what the video itself says. |
| `file_edit` | Corrects a file's recorded quality, languages or release group. |
| `file_delete` | Deletes episode files from disk. |
| `import_scan` | Says what Sonarr makes of the files in a folder. |
| `import_apply` | Imports files as the episodes they are. |

## Searching and downloads

| Tool | What it does |
|---|---|
| `release_search` | Every release for an episode or season, with Sonarr's reason for rejecting each. |
| `release_grab` | Sends a release to the download client, rejected or not. |
| `release_parse` | What Sonarr reads from a release or file name. |
| `wanted_search` | Searches for everything missing, or everything below cutoff. |
| `queue_list` | The download queue, and where Sonarr is with each download. |
| `queue_grab` | Sends now what a delay profile is holding back. |
| `queue_remove` | Removes downloads, from the download client too, and blocklists the release if asked. |
| `history_list` | What happened to the library's episodes, to one series' or to one episode's. |
| `history_mark_failed` | Marks a grab as failed, so Sonarr blocklists it and looks again. |
| `blocklist_list` | The releases Sonarr will not grab again, and why. |
| `blocklist_remove` | Takes releases off the blocklist. |

## Sonarr itself

| Tool | What it does |
|---|---|
| `server_info` | Checks the connection and gives Sonarr's version. |
| `server_health` | Sonarr's own health checks. |
| `server_disk_space` | The disks Sonarr sees, and the space free on each. |
| `server_updates` | The releases available, and which one is running. |
| `server_restart` | Restarts Sonarr. |
| `server_shutdown` | Shuts Sonarr down. Nothing here can start it again. |
| `task_list` | Sonarr's scheduled tasks, and when each runs. |
| `task_run` | Runs a scheduled task now. |
| `command_list` | What Sonarr is doing and has just done, or one command by its id. |
| `command_cancel` | Cancels a command that has not started. |
| `log_list` | Recent log entries, warnings and above unless asked. |
| `logfile_list` | Sonarr's log files on disk. |
| `logfile_read` | The end of a log file, or its lines with some text in them. |
| `filesystem_list` | Folders as Sonarr sees them, which is not always as the host does. |
| `backup_list` | The backups Sonarr holds. |
| `backup_create` | Backs up Sonarr's database and settings now. |
| `backup_restore` | Restores from a backup. Sonarr must then be restarted. |
| `backup_delete` | Deletes a backup. |

## Settings

| Tool | What it does |
|---|---|
| `config_get` | Reads one section of the settings: naming, media management, host, UI, or the indexer, download client and import list options. |
| `config_edit` | Changes one section of the settings, and says what changed. |
| `config_naming_preview` | Shows what a naming format would name things, before it is set. |
| `language_list` | The languages Sonarr knows. |
| `tag_list` | Every tag, and what uses it. |
| `tag_create` | Creates a tag. |
| `tag_rename` | Renames a tag. |
| `tag_delete` | Deletes a tag nothing uses. |
| `rootfolder_list` | The root folders, their free space and how many series each holds. |
| `rootfolder_add` | Adds a root folder. |
| `rootfolder_remove` | Stops Sonarr using a root folder. Nothing on disk is touched. |

## Quality

| Tool | What it does |
|---|---|
| `profile_list` | The quality profiles, and how many series are on each. |
| `profile_create` | Creates a quality profile, from scratch or as a copy of another. |
| `profile_edit` | Changes the qualities allowed, the cutoff, and the score each custom format gets. |
| `profile_delete` | Deletes a profile no series is on. |
| `qualitydefinition_list` | Each quality's name in file names, and the sizes a release of it may be. |
| `qualitydefinition_edit` | Changes those names and sizes. |
| `customformat_list` | The custom formats, and which profiles score them. |
| `customformat_add` | Adds a custom format. |
| `customformat_edit` | Changes a custom format. |
| `customformat_delete` | Deletes a custom format. |

## Providers

Indexers, download clients, notifications and import lists each have the same six tools.

| Tool | What it does |
|---|---|
| `indexer_list`, `downloadclient_list`, `notification_list`, `importlist_list` | What is set up, without its credentials. |
| `indexer_kinds`, `downloadclient_kinds`, `notification_kinds`, `importlist_kinds` | The kinds that can be added, and the settings each takes. |
| `indexer_add`, `downloadclient_add`, `notification_add`, `importlist_add` | Adds one. |
| `indexer_edit`, `downloadclient_edit`, `notification_edit`, `importlist_edit` | Changes one. Only what is given changes. |
| `indexer_test`, `downloadclient_test`, `notification_test`, `importlist_test` | Does what Sonarr's Test button does, and says what is wrong with each that fails. |
| `indexer_delete`, `downloadclient_delete`, `notification_delete`, `importlist_delete` | Deletes one, with its credentials. |
| `exclusion_list`, `exclusion_add`, `exclusion_remove` | The shows import lists may never add. |
| `metadata_list`, `metadata_edit` | The files Sonarr writes beside episodes for a media player to read. |

`notification_test` sends a real message to wherever the notification points.

## Rules Sonarr follows

Each of these has a `_list`, `_add`, `_edit` and `_delete` tool.

| Tools | What they hold |
|---|---|
| `delayprofile_list`, `delayprofile_add`, `delayprofile_edit`, `delayprofile_delete` | How long Sonarr waits before grabbing, so a better release has time to appear. |
| `releaseprofile_list`, `releaseprofile_add`, `releaseprofile_edit`, `releaseprofile_delete` | Words a release's name must have, or must not have. |
| `remotepathmapping_list`, `remotepathmapping_add`, `remotepathmapping_edit`, `remotepathmapping_delete` | Where a download client on another machine puts its downloads, as Sonarr sees it. |
| `autotag_list`, `autotag_add`, `autotag_edit`, `autotag_delete` | Rules that tag the series that meet their conditions. |
| `filter_list`, `filter_add`, `filter_edit`, `filter_delete` | The web interface's saved filters. |

## Settings records

Every kind of record Sonarr keeps in its settings is handled the one way.

- **Answers use Sonarr's own names.** A record's properties go by the names Sonarr gives them, and its tags by label. A provider's own settings, such as its address, host and category, sit under `settings`.
- **Changes use the same names.** `set` changes properties, and `settings` changes a provider's own. A name the record does not have is refused, with the ones it has.
- **New providers are turned on.** A new indexer or download client is enabled, as one added on Sonarr's own page is. A notification is sent for the events set, and no others.
- **Providers are tested before they are saved,** unless `force` is set. That goes for one being added as well as one being changed.
- **Credentials can be set, and are never shown.**

## Text from outside

Much of what a tool answers was written by someone else: a release name from a public indexer, a title and plot from TheTVDB, a download client's message.

- **Hidden characters are taken out.** That covers control characters, ones with no width, and ones that change the direction text runs in.
- **Each piece of text is cut at 2000 characters.** Nothing can be tucked into a name, or buried in a page of text.
- **Paths and ids are left alone.** A path, a folder, a release's `guid` and a download's id stay exactly as Sonarr holds them, since a later call hands them back.
- **It is still someone else's text.** A client is told, when it connects, to treat it as data and not as instructions.

## A log of changes

With `SONARR_LOG=INFO`, each call of a tool that changes Sonarr is logged as one line: the tool, its arguments, and whether it was refused, failed or answered.

- What a record is saved with (`settings`, `set`) is logged by name only, each value blanked, since some of it is a credential.
- An argument named as a credential is blanked too.
- An address is logged without who it signs in as, or the value of any parameter.

`SONARR_LOG=TRACE` logs every request and answer, blanked the same way.
