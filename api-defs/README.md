# api-defs

The vendored OpenAPI document, the definitions imported from it, and what Sonarr does that its document does not say.

## API spec

Sonarr publishes an OpenAPI document for its v3 API (which v4 still serves), vendored here as `sonarr-openapi-<version>.json`, the reference for `sdk/sonarr` - and it is a build input. `sdk/pandorest`, on [pandorest](https://github.com/katbyte/pandorest), imports it into checked-in definitions under `api-defs/sonarr-<version>/` beside it, fixing the document's known bugs with named workarounds on the way, and generates the client from those definitions. `make generate` runs both steps, `make pandorest-diff` reports what a refreshed document would change, `make gencheck` (and the unit tests) fail when the generated code is stale, and `make apicheck` proves every operation in the spec has a method.

| File | Source | Version vendored |
|---|---|---|
| `sonarr-openapi-4.0.20.3014.json` | `src/Sonarr.Api.V3/openapi.json` in Sonarr's source, at the release's tag: <https://raw.githubusercontent.com/Sonarr/Sonarr/v4.0.20.3014/src/Sonarr.Api.V3/openapi.json> | Sonarr 4.0.20.3014 - 162 paths, 234 operations, of which 6 serve the web interface and are dropped, leaving 228 |

The file is named by the Sonarr release it was taken from, not by the version the document gives itself, which is 3.0.0 for every release: the generated package has both, as `DocumentVersion` and `APIVersion`. It is written by Swashbuckle from the controllers each release, so the copy to vendor is the one at the tag matching the container image the live suites run (`SONARR_TEST_IMAGE` in `.scripts/testenv.sh`). To refresh: vendor the new tag's file beside the old one under its own version, run `make generate`, read `go run ./sdk/pandorest diff -old api-defs/sonarr-<old> -new api-defs/sonarr-<new>` to see what changed in API terms (breaking changes are marked), and delete the old pair once the new one is in. A workaround whose bug the new document fixes fails the import and names itself; delete it.

The spec is documentation of intent, not of behaviour: the live suites (`acceptance/sonarr`, `acceptance/tools`) are what prove the shapes against a real Sonarr, and the quirks they found are recorded below.

## Where the spec is wrong

These are shape bugs, fixed in the generated client by the importer's workarounds (`sdk/pandorest/workarounds/`, listed in `api-defs/sonarr-<version>/Service.json`). Each checks its bug is still in the document and fails the import once it is not.

- **The web interface's routes are in it.** `/`, `/{path}`, `/content/{path}`, `/login` and `/logout` answer HTML to a browser. They are dropped.
- **Creates answer 201 and updates 202.** Every operation is documented as answering 200; the creates answer 201 Created and the updates 202 Accepted, with the same body. A client that trusted the document would treat every successful save as an error.
- **Fourteen GETs say they answer nothing.** They answer JSON (the API's version list, the custom format and auto-tagging schemas, the file system browser, a series' folder name, the naming examples) or a file (a log file, the routing graph, a poster, the iCal feed). Each is declared with what it really answers. The file system's answers and a series' folder name are records the document has no name for, so those records are declared too.
- **Writes that answer what they changed say they answer nothing.** Setting episodes monitored, editing files in bulk, the series editor, importing series, reprocessing a manual import and grabbing a release all answer the records they changed; those answers are declared, so a tool can report what Sonarr did rather than what it asked for.
- **A command's own fields have nowhere to go.** `POST /api/v3/command` is documented as taking the command resource, but Sonarr reads the command's own fields (`seriesId`, `episodeIds`, `files`...) from the top level of the body. The body is left open so a command can carry them.
- **Nothing says what a command is called or which fields it reads.** Every command goes through that one open body. The eleven the tools queue are declared (`sdk/pandorest/workarounds/commands.go`), each with the fields Sonarr's own command reads, so the client has a method for each that names the command and takes its fields, and a form of it that waits for the command to end. Any other command is still posted by name.
- **An update's id is typed as text.** The `{id}` of a PUT path is a string in the document, and a number on the GET and DELETE of the same path. It is a number in every one.
- **An id says nothing of what it is the id of.** Every id is an `integer`, so a series' id and an episode's are the same type. Seven kinds that travel between calls are declared (`sdk/pandorest/workarounds/ids.go`): a series, an episode, an episode file, a tag, a quality profile, a command and TheTVDB's id of a series. The import fails when a declaration names nothing in the document.
- **Testing every provider answers each result, and a 400 when any fails.** `POST .../testall` is documented as answering nothing; it answers the result for each indexer, download client or other provider, as a 400 when any of them fails. The results are declared on both, so the client hands back the results rather than an error when a provider fails.
- **Bulk updates answer a list.** `PUT .../bulk` for indexers, download clients, import lists and custom formats is documented as answering one resource and answers every resource it changed.
- **An air date is declared as plain text.** An episode's air date, and the one a release title names for a daily show, are always a day written `2026-09-19`. They are declared as days, so the client reads them as dates.
- **A URL is written as a string.** `HttpUri` (a health check's wiki link) is documented as an object of its parts and written as the URL, so nothing holding one would decode.

One fix is the generator's rather than a workaround's, because the document is right about the shape and silent about the behaviour: **settings are saved field by field.** A settings section (naming, media management, host, the indexer and download client options...) is read whole and written back whole, and a field left out of the write arrives as null - the host settings answer 500 to one, and the others keep the old value rather than the empty one sent. The generated settings models (`config.Service.WrittenWhole`) always send their strings and numbers, empty or not.

Dates are read as dates (`config.Service.Dates`). Sonarr writes each as RFC 3339 in UTC, `2026-09-19T13:00:00Z`, and year one for never, which the client reads as no date.

The importer also prefers JSON wherever an operation lists JSON alongside `text/plain` and `text/json` (Swashbuckle's usual trio), so those operations are typed rather than treated as files.

## Server behaviour the tools work around

Not shape bugs, but behaviour a tool has to know about to answer truthfully, each found by the live suites.

- **A grab reaches the queue only once Sonarr checks its download clients.** Straight after a search, the queue can lag the grab by a minute. The tools that report the queue after a search ask Sonarr to check its download clients first (`RefreshMonitoredDownloads`).
- **With Rename Episodes off, Sonarr proposes no renames.** It keeps the names files arrive with, so its rename preview is empty however far the names are from the format. `audit_naming` says so rather than reporting a clean library, and `series_rename` refuses rather than claiming there was nothing to do.
- **Quality comes from the name, resolution from the video.** Sonarr records what the file name claims and reads the resolution from the file's streams. `audit_quality_mismatch` compares the two; Sonarr itself never does.
- **Cutoff Unmet is quality alone.** Sonarr's own list compares each file's quality with its profile's cutoff and never looks at custom format scores, so a file good enough on quality and short of the score its profile upgrades until is in no list Sonarr shows. `audit_cutoff_unmet` sweeps for those itself.
- **The built-in quality profiles do not allow upgrades.** Sonarr creates Any, SD, HD-720p, HD-1080p, Ultra-HD and HD - 720p/1080p with Upgrades Allowed off, and with it off the Cutoff Unmet list is measured against the profile's *lowest* allowed quality instead of its cutoff. The audit says so in the finding, because Sonarr will replace none of those files by itself.
- **Disk scans do not check for samples.** A five-minute file dropped into a series folder is taken as the episode on the next rescan, which is what `audit_runtime` is for.
- **Imports from a rescan leave no history.** Only grabs and imports of downloads are in the history, so the failure audits only judge what was downloaded.
- **Reading several files by id fails on one unknown id.** Asked for a list, Sonarr answers 500 when any id in it is unknown, so `file_delete` reads each file by its own id before deleting any, and names one that does not exist.
- **Credentials come back masked, marked by privacy level.** Each provider setting says whether it is a password, API key or user name; the list tools leave those out entirely rather than show the mask.
- **A tag in use cannot be deleted.** Sonarr refuses, and `tag_delete` passes its reason on; `tag_list` shows what carries each tag.
- **Delay profiles do not hold back a search you asked for.** Only grabs from the RSS sync wait out a delay, so `queue_grab` is for those.
- **A file already in its series folder is imported where it is.** A manual import of a file from elsewhere moves or copies it into the series folder and names it to the format; one already inside is taken in where it is.
- **TheTVDB titles carry disambiguating suffixes.** "The Office (US)" and "The Office (2001)"; `series_add` matches a title with or without them and lists the candidates when a name fits more than one show.

## Conventions worth knowing

- API key auth is the `X-Api-Key` header. The key is in Settings → General → Security, or in the `ApiKey` element of `config.xml`.
- Errors come back as a list of validation failures (`propertyName`, `errorMessage`) or as a message; the SDK puts either on the error (`sdk/sonarr/service.go`), so a refused write says why.
- Paged lists (`/wanted/missing`, `/wanted/cutoff`, `/queue`, `/history`, `/blocklist`, `/log`, `/importlistexclusion/paged`) take `page` and `pageSize` and answer `records` and `totalRecords`; each has a `Complete` method that reads every page.
- Commands run asynchronously: `POST /api/v3/command` answers at once with the command queued, and `GET /api/v3/command/{id}` reports its progress. The tools that start one wait a bounded time for it to finish and say whether it did.
