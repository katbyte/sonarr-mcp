# A typical session

How a library is put right, one audit at a time. Load the `curation` toolset for it: `SONARR_TOOLSETS=curation`.

1. **Start with the counts.** `audit_all` says where the library needs work.
2. **Fetch what never arrived.** `audit_missing_episodes` lists what aired and has no file. `episode_search`, `season_search` or `wanted_search` go and get it, and report what reached the queue.
3. **Deal with series that have nothing.** `audit_no_files` names each one. It says whether the series is to be fetched, is already in its folder waiting to be imported, or will never be fetched as it stands.
4. **Clear what is stuck.** `audit_stuck_downloads` lists stuck downloads, with Sonarr's reason. A download Sonarr could not match is imported with `import_apply`, naming the series and episodes. A bad release is removed with `queue_remove` and `blocklist`, and Sonarr searches for another.
5. **Take in what Sonarr does not know.** `audit_untracked_files` and `audit_unmapped_folders` find what is on disk and not in Sonarr. `import_apply` and `series_import` take it in, where it is.
6. **Clear records with nothing behind them.** `audit_missing_files` and `audit_missing_folders` find them, and say whether a folder moved or is gone. `series_edit` with `path`, then `series_rescan`, puts a moved one right. `series_rescan` alone clears one that is gone.
7. **Correct the quality on record.** `audit_quality_mismatch` finds files recorded at the wrong quality. `file_edit` corrects them. Sonarr then upgrades what it should, and stops chasing what it should not.
8. **Rename to the format.** `audit_naming` shows what does not match the naming format. `series_rename` with `dry_run` shows the renames, and without it makes them.
9. **Fix the settings that miss things.** `audit_monitoring`, `audit_series_settings` and `audit_profiles` find settings that will quietly miss episodes, or do nothing. `series_edit`, `season_monitor` and `tag_delete` fix them.

What each audit looks for is in [audits.md](audits.md). Every tool named here is in [tools.md](tools.md).
