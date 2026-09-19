# docs

## API spec

Sonarr publishes an OpenAPI document for its v3 API (which v4 still serves),
vendored here as the reference for `lib/sonarr` - and it is a build input.
`internal/pandorest` (see its [README](../internal/pandorest/README.md))
imports it into checked-in definitions under `api-definitions/sonarr/`,
fixing the document's known bugs with named workarounds on the way, and
generates the client from those definitions. `make generate` runs both steps,
`make pandorest-diff` reports what a refreshed document would change, `make
gencheck` (and the unit tests) fail when the generated code is stale, and
`make apicheck` proves every operation in the spec has a method.

| File | Source | Version vendored |
|---|---|---|
| `sonarr-openapi.json` | `src/Sonarr.Api.V3/openapi.json` in Sonarr's source, at the release's tag: <https://raw.githubusercontent.com/Sonarr/Sonarr/v4.0.20.3014/src/Sonarr.Api.V3/openapi.json> | Sonarr 4.0.20.3014 - 162 paths, 234 operations, of which 6 serve the web interface and are dropped, leaving 228 |

The file is written by Swashbuckle from the controllers each release, so the
copy to vendor is the one at the tag matching the container image the live
suites run (`SONARR_TEST_IMAGE` in `scripts/testenv.sh`). To refresh: fetch
the new tag's file, run `make pandorest-diff` to see what changed in API terms
(breaking changes are marked), then `make generate` and review the diff. A
workaround whose bug the new document fixes fails the import and names
itself; delete it.

The spec is documentation of intent, not of behaviour: the live suites
(`integration/`, `acceptance/`) are what prove the shapes against a real
Sonarr, and the quirks they found are recorded below.

## Where the spec is wrong

These are shape bugs, fixed in the generated client by the importer's
workarounds (`internal/pandorest/importer/workarounds/sonarr.go`, listed in
`api-definitions/sonarr/Service.json`). Each checks its bug is still in the
document and fails the import once it is not.

- **The web interface's routes are in it.** `/`, `/{path}`, `/content/{path}`,
  `/login` and `/logout` answer HTML to a browser. They are dropped.
- **Creates answer 201 and updates 202.** Every operation is documented as
  answering 200; the creates answer 201 Created and the updates 202 Accepted,
  with the same body. A client that trusted the document would treat every
  successful save as an error.
- **Fourteen GETs say they answer nothing.** They answer JSON (the API's
  version list, the custom format and auto-tagging schemas, the file system
  browser, a series' folder name, the naming examples) or a file (a log file,
  the routing graph, a poster, the iCal feed). Each is declared with what it
  really answers.
- **Writes that answer what they changed say they answer nothing.** Setting
  episodes monitored, editing files in bulk, the series editor, importing
  series, reprocessing a manual import and grabbing a release all answer the
  records they changed; those answers are declared, so a tool can report what
  Sonarr did rather than what it asked for.
- **A command's own fields have nowhere to go.** `POST /api/v3/command` is
  documented as taking the command resource, but Sonarr reads the command's
  own fields (`seriesId`, `episodeIds`, `files`...) from the top level of the
  body. The body is left open so a command can carry them.
- **Testing every provider answers each result, and a 400 when any fails.**
  `POST .../testall` is documented as answering nothing; it answers the
  result for each indexer, download client or other provider, as a 400 when
  any of them fails. The results are declared on both, so the client hands
  back the results rather than an error when a provider fails.
- **Bulk updates answer a list.** `PUT .../bulk` for indexers, download
  clients, import lists and custom formats is documented as answering one
  resource and answers every resource it changed.
- **A URL is written as a string.** `HttpUri` (a health check's wiki link) is
  documented as an object of its parts and written as the URL, so nothing
  holding one would decode.

One fix is the generator's rather than a workaround's, because the document
is right about the shape and silent about the behaviour: **settings are saved
field by field.** A settings section (naming, media management, host, the
indexer and download client options...) is read whole and written back
whole, and a field left out of the write arrives as null - the host settings
answer 500 to one, and the others keep the old value rather than the empty
one sent. The generated settings models (`config.Service.WrittenWhole`)
always send their strings and numbers, empty or not.

The importer also prefers JSON wherever an operation lists JSON alongside
`text/plain` and `text/json` (Swashbuckle's usual trio), so those operations
are typed rather than treated as files.

## Server behaviour the tools work around

Not shape bugs, but behaviour a tool has to know about to answer truthfully,
each found by the live suites.

- **A grab reaches the queue only once Sonarr checks its download clients.**
  Straight after a search, the queue can lag the grab by a minute. The tools
  that report the queue after a search ask Sonarr to check its download
  clients first (`RefreshMonitoredDownloads`).
- **With Rename Episodes off, Sonarr proposes no renames.** It keeps the names
  files arrive with, so its rename preview is empty however far the names are
  from the format. `audit_naming` says so rather than reporting a clean
  library, and `series_rename` refuses rather than claiming there was nothing
  to do.
- **Quality comes from the name, resolution from the video.** Sonarr records
  what the file name claims and reads the resolution from the file's streams.
  `audit_quality_mismatch` compares the two; Sonarr itself never does.
- **Disk scans do not check for samples.** A five-minute file dropped into a
  series folder is taken as the episode on the next rescan, which is what
  `audit_runtime` is for.
- **Imports from a rescan leave no history.** Only grabs and imports of
  downloads are in the history, so the failure audits only judge what was
  downloaded.
- **Reading several files by id fails on one unknown id.** Asked for a list,
  Sonarr answers 500 when any id in it is unknown, so `file_delete` reads each
  file by its own id before deleting any, and names one that does not exist.
- **Credentials come back masked, marked by privacy level.** Each provider
  setting says whether it is a password, API key or user name; the list tools
  leave those out entirely rather than show the mask.
- **A tag in use cannot be deleted.** Sonarr refuses, and `tag_delete` passes
  its reason on; `tag_list` shows what carries each tag.
- **Delay profiles do not hold back a search you asked for.** Only grabs from
  the RSS sync wait out a delay, so `queue_grab` is for those.
- **A file already in its series folder is imported where it is.** A manual
  import of a file from elsewhere moves or copies it into the series folder
  and names it to the format; one already inside is taken in where it is.
- **TheTVDB titles carry disambiguating suffixes.** "The Office (US)" and "The
  Office (2001)"; `series_add` matches a title with or without them and lists
  the candidates when a name fits more than one show.

## Conventions worth knowing

- API key auth is the `X-Api-Key` header. The key is in Settings → General →
  Security, or in the `ApiKey` element of `config.xml`.
- Errors come back as a list of validation failures (`propertyName`,
  `errorMessage`) or as a message; `lib/client` puts either on the error, so a
  refused write says why.
- Paged lists (`/wanted/missing`, `/wanted/cutoff`, `/queue`, `/history`,
  `/blocklist`, `/log`, `/importlistexclusion/paged`) take `page` and
  `pageSize` and answer `records` and `totalRecords`; each has a `Complete`
  method that reads every page.
- Commands run asynchronously: `POST /api/v3/command` answers at once with the
  command queued, and `GET /api/v3/command/{id}` reports its progress. The tools
  that start one wait a bounded time for it to finish and say whether it did.
