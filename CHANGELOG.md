# Changelog

## Unreleased

The first release: an MCP server, CLI and Go SDK for running and auditing a Sonarr library.

- **16 audits and `audit_all`**, each a sweep over the whole library for one thing that goes wrong, returning a worklist that names the tool that fixes it: episodes that aired and never arrived, files below their profile's cutoff, downloads stuck or failing, files and folders Sonarr has lost track of or never knew, a folder renamed or moved out from under it, names off the format, files recorded at the wrong quality or running the wrong length or in the wrong language, monitoring that will miss new seasons, series set up wrong, settings that do nothing, and Sonarr's own health checks.
- **69 tools in toolsets**: series, seasons, episodes and files; searching, grabbing and the queue; history and the blocklist; imports, renames, rescans and refreshes; tags, profiles, custom formats and root folders; indexers and download clients, listed and tested; tasks, commands and logs. The default `core` set is six read-only tools, about 850 tokens; `curation` loads the audits and everything that fixes what they find.
- **Deleting is opt-in.** `series_delete` and `file_delete` are only registered with `--enable-delete`, and `--read-only` registers the 43 read tools and nothing else.
- **`lib/sonarr`**: a complete Go client for Sonarr's v3 API, all 228 operations, generated from Sonarr's own OpenAPI document with the document's bugs fixed by named workarounds, each of which fails the import once Sonarr fixes its bug. The tests call 224 of the 228 against a real Sonarr; the other four restart, stop or restore it.
- **Tested against a real Sonarr 4.0.20** in Docker, with a fake indexer and a fake SABnzbd the tests drive, so searches really grab and downloads really import. The suite fails if a registered tool has no test, or if an audit can report a kind of finding the seeded library never produced and nothing explains. Sonarr's calls to TheTVDB and its own services are recorded and replayed, so the tests need no network.
- Serves MCP over stdio, or over HTTP with a bearer token (`--listen`), and ships as a container image.
