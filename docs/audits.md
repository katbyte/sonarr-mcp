# The audits

Each audit checks the whole library for one thing that goes wrong. It answers with a list of findings, and each finding names the tool that fixes it.

## How an audit answers

- **One series or all.** Every audit takes `series`, to check one series alone.
- **A limit.** `limit` is how many findings to list, 100 unless given. `total_findings` is the real count, so nothing past the limit goes unnoticed.
- **A finding.** Each gives the series, what it is about (`subject`), the kind of problem (`problem`), the `detail` and the `fix`.
- **Kinds of problem.** `problem` is a short fixed phrase, so findings can be grouped by it. Each audit's phrases are listed below as "Reports".

## audit_all

Runs every audit in one call and gives only the counts. One call says where the library needs work. Start here, then call the audit whose count is not zero for its list.

An audit that could not run is listed under `failed` with its error, and has no count. That means unknown, not none.

## audit_missing_episodes

Monitored episodes that have aired and have no file, by series and season. It says which are already downloading, and which Sonarr searched for and did not find.

- **Reports:** `episodes missing`, `whole season missing`.
- **Fix with:** `episode_search`, `season_search` or `series_search`, or `wanted_search` for the whole library.

## audit_no_files

Series Sonarr records no file for at all. Each is one finding, with the series' folder, and says which of five things it is.

| Reports | What it means | Fix with |
|---|---|---|
| `no files` | Wanted, and never fetched. | `series_search` |
| `no files and nothing wanted` | Nothing of it is monitored, so nothing will ever be fetched. | `series_edit` or `season_monitor`, then `series_search` |
| `files in the folder, none tracked` | Its folder holds video files, and Sonarr tracks none. | `import_scan`, then `import_apply` |
| `no episodes known` | Sonarr lists no episode for it. | `series_refresh` |
| `no air dates` | No episode has an air date, so Sonarr never looks for one. | `series_refresh`, for the dates |

A series that has not aired yet is left out: there is nothing to fetch until it does. A series with some of its files is `audit_missing_episodes`' finding.

## audit_cutoff_unmet

Monitored files below their profile's cutoff, by series, each with its current quality. A file counts when it is below the cutoff quality. It also counts when it is below the custom format score the profile upgrades until, which Sonarr's own Cutoff Unmet list leaves out.

It says when a profile has Upgrades Allowed off, as Sonarr's built-in profiles do. Nothing is then replaced by itself, so turn upgrades on first.

- **Reports:** `below quality cutoff`, `below custom format cutoff`.
- **Fix with:** `series_search` or `episode_search`, or `wanted_search` with `kind` set to `cutoff`. `profile_list` shows the cutoffs.

## audit_stuck_downloads

Downloads in the queue that need a person:

- blocked from importing, with Sonarr's reason
- waiting to import for hours
- failed, paused, or flagged with a warning or an error
- on a download client Sonarr cannot reach

- **Reports:** `cannot import`, `download failed`, `waiting to import`, `download client unreachable`, `paused in the download client`, `error`, `warning`, `not starting`.
- **Fix with:** `queue_remove`, with `blocklist` to get a different release. `import_scan` and `import_apply` import one Sonarr could not match.

## audit_failed_downloads

Download failures over the last 30 days, by episode. It flags the episodes whose releases keep failing.

It also lists grabs that went nowhere: sent to the download client, never imported, never failed, and gone from the queue.

- **Reports:** `repeated failures`, `download failed`, `grab went nowhere`.
- **Fix with:** `release_search` to pick a release by hand, or `history_mark_failed` for a bad grab.

## audit_missing_files

Files Sonarr records that are no longer on disk: deleted, or moved outside Sonarr. The episodes show as downloaded when they are not.

A series whose whole folder is gone is `audit_missing_folders`' finding.

- **Reports:** `files gone from disk`, `folder holds no files`.
- **Fix with:** `series_rescan`, which drops the records so the episodes count as missing again.

## audit_untracked_files

Video files in series folders that Sonarr is not tracking: copied in by hand, a second copy of an episode, or a name it cannot read. Each comes with what Sonarr reads from the name, and why it has not taken the file in.

Past `limit`, files are counted and Sonarr is not asked about them. That keeps the audit quick on a big library.

- **Reports:** `cannot tell which episode`, `not imported`, `rejected`, `file not tracked`.
- **Fix with:** `import_apply`, or `series_rescan` after fixing a name. `import_scan` shows one folder.

## audit_unmapped_folders

Folders in the root folders that are not a series in Sonarr, and how many video files each holds. They are shows copied in by hand, or left behind when a series was deleted.

A folder may go by the name of a series whose own folder is gone. It is then named as that series, moved. Point the series at it rather than adding it again.

- **Reports:** `folder not in Sonarr`, `folder of a series that moved`.
- **Fix with:** `series_import`, which matches each folder to a show and adds it with its files, where they are.

## audit_missing_folders

Series whose folder is not on disk, although Sonarr records files in it. The folder was renamed or moved outside Sonarr, or its drive is not mounted.

- A drive that is not mounted is reported once, for the root folder, and not once for each series.
- When a folder Sonarr does not know goes by the series' name, the finding names it.
- A series with no files has no folder until Sonarr imports one, and is not a finding.

- **Reports:** `series folder renamed`, `series folder missing`, `root folder holds none of its series`.
- **Fix with:** `series_edit` with `path`, then `series_rescan`.

## audit_naming

Series whose files are not named to Sonarr's naming format, with a sample of the renames.

With Rename Episodes off, Sonarr keeps the names files arrive with and cannot say which differ. The audit says so instead.

- **Reports:** `files not named to the format`, `renaming is off`.
- **Fix with:** `series_rename`. With `dry_run` it only shows the renames.

## audit_quality_mismatch

Files recorded as one quality whose video is another:

- recorded 1080p but really 720p, which Sonarr will never upgrade
- labelled SD but really HD, which Sonarr keeps trying to upgrade

Sonarr reads the resolution from the video when it imports. So a mismatch means the quality was set by hand, or the file was replaced on disk since.

- **Reports:** `labelled better than it is`, `labelled worse than it is`.
- **Fix with:** `file_edit`, to record the real quality.

## audit_runtime

Files whose running time is far off the episode's, or that have none at all. A short file is a download cut off, a sample imported as the episode, or the wrong episode.

- Short files come first. A long one may be the right file: two parts in one, or an extended cut. Check it before replacing it.
- A file holding several episodes is held to their total.
- Specials are left out.
- `tolerance_percent` sets how far off counts, 40 unless given.

- **Reports:** `shorter than it should be`, `longer than it should be`, `no running time`.
- **Fix with:** `release_search`, to replace one.

## audit_language

Files not in the language they should be: audio only in other languages, a file recorded as a dub, or one whose language Sonarr could not tell.

Each series is checked against its original language, or against `language` when that is given.

- **Reports:** `no audio in the language`, `recorded in another language`, `language unknown`.
- **Fix with:** `file_edit` when the record is wrong, or `release_search` for a release in the right language.

## audit_monitoring

Monitoring that will quietly miss episodes:

- a continuing series that will not monitor new seasons
- a continuing series that is not monitored
- a monitored series with every season unmonitored
- a latest season left unmonitored

- **Reports:** `continuing series not monitored`, `nothing monitored`, `new seasons will not be monitored`, `latest season not monitored`.
- **Fix with:** `series_edit` or `season_monitor`.

## audit_series_settings

Series settings that look wrong:

- anime not typed as anime, so absolute numbering fails
- a daily show not typed as daily
- a folder name that does not match the naming format
- a series outside every root folder

When most of the library's folders are off the format, it says so once, with the format to set, rather than listing every series.

- **Reports:** `anime not typed anime`, `daily show not typed daily`, `folder not named to the format`, `outside every root folder`.
- **Fix with:** `series_edit`.

## audit_profiles

Settings that do nothing:

- quality profiles no series uses
- tags nothing uses
- custom formats no profile scores
- release profiles that are switched off
- delay profiles, release profiles, indexers and download clients scoped to tags no series has

- **Reports:** `quality profile unused`, `tag unused`, `custom format scored nowhere`, `release profile disabled`, `tag scopes settings but no series`.
- **Fix with:** `tag_delete` and `series_edit`. A release profile is put right with `releaseprofile_edit` or `releaseprofile_delete`.

## audit_health

Sonarr's own health checks: indexers and download clients failing, root folders missing, remote path mappings wrong. It adds root folders that are running out of space.

- **Reports:** `health` with Sonarr's own level after it, such as `health warning`. Also `root folder unreachable` and `root folder low on space`.
- **Fix with:** what each finding names. For a full disk that is freeing space, or `rootfolder_add` and moving series with `series_edit`.

## audit_server

Sonarr itself, rather than the library:

- no backup, or none for over a week
- a release newer than the one running
- the errors it logged in the last day, grouped by what logged them, with the newest of each

A Sonarr that runs in a container is told to update by replacing its image.

- **Reports:** `no recent backup`, `update available`, `errors in the log`.
- **Fix with:** `backup_create` for a backup. `server_updates` lists what an update changes, and `log_list` shows each error.
