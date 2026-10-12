# pandorest

[![GitHub release](https://img.shields.io/github/v/release/katbyte/pandorest?color=blueviolet)](https://github.com/katbyte/pandorest/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/pandorest?color=00ADD8)](https://github.com/katbyte/pandorest/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/pandorest?color=blue)](https://github.com/katbyte/pandorest/blob/main/LICENSE)
![build](https://github.com/katbyte/pandorest/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/katbyte/pandorest/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/pandorest/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/pandorest/badges/coverage.json)](https://github.com/katbyte/pandorest/actions/workflows/coverage.yaml)

pandorest generates typed Go Services from vendored OpenAPI documents. It was written in embyfin-mcp for the Emby, Jellyfin and TMDB Services, copied into three more repositories, and is now here so they share one.

Inspired by [Pandora](https://github.com/hashicorp/pandora), the [go-azure-sdk](https://github.com/hashicorp/go-azure-sdk) generator: the same importer, definitions, differ and generator pipeline, scaled down to a handful of documents in one repository:

```text
api-defs/
  emby-openapi-4.10.0.40.json ──┐               ┌─ emby-4.10.0.40/*.json ──┐
  jellyfin-openapi-12.1.0.json ─┼─ import ──────┼─ jellyfin-12.1.0/*.json ─┼─ generate ─ sdk/emby, sdk/jf, sdk/tmdb
  tmdb-openapi-2026.09.22.json ─┘ (workarounds) └─ tmdb-2026.09.22/*.json ─┘
                                                        │
                                         diff ──────────┘  (what a refreshed spec changes)
```

It does what the alternatives were tried and rejected for (oapi-codegen, openapi-generator): errors for undocumented statuses rather than a result per content type, plain string ids, readable names for machine-made operationIds, no builder chains or nullable wrappers, and a test for every operation it writes.

## The Services it generates

Each lives in the repository that vendors its document, beside the MCP server built on it. The version is the server release the document was taken from.

| Server | SDK | Repository |
|---|---|---|
| Emby 4.10.0.40 | `github.com/katbyte/embyfin-mcp/sdk/emby` | [embyfin-mcp](https://github.com/katbyte/embyfin-mcp) |
| Jellyfin 12.1.0 | `github.com/katbyte/embyfin-mcp/sdk/jf` | [embyfin-mcp](https://github.com/katbyte/embyfin-mcp) |
| TMDB (document of 2026-09-22) | `github.com/katbyte/embyfin-mcp/sdk/tmdb` | [embyfin-mcp](https://github.com/katbyte/embyfin-mcp) |
| Sonarr 4.0.20.3014 | `github.com/katbyte/sonarr-mcp/sdk/sonarr` | [sonarr-mcp](https://github.com/katbyte/sonarr-mcp) |
| Radarr 6.4.4.10685 | `github.com/katbyte/radarr-mcp/sdk/radarr` | [radarr-mcp](https://github.com/katbyte/radarr-mcp) |
| Prowlarr 2.6.5.5623 | `github.com/katbyte/prowlarr-mcp/sdk/prowlarr` | [prowlarr-mcp](https://github.com/katbyte/prowlarr-mcp) |
| Dockhand 1.0.51 | `github.com/katbyte/dockhand-mcp/sdk/dockhand` | [dockhand-mcp](https://github.com/katbyte/dockhand-mcp) |

## Using it

pandorest is a library. A repository that generates an SDK keeps what is its own, the documents, the services they describe and the workarounds for their bugs, and a main of a few lines that hands them over:

```go
// sdk/pandorest/main.go
package main

import (
	"github.com/katbyte/pandorest"
	"github.com/katbyte/pandorest/config"

	"github.com/katbyte/sonarr-mcp/sdk/pandorest/workarounds"
)

func main() {
	pandorest.Main(pandorest.Config{
		Services: []config.Service{{
			Name:         "sonarr",
			Package:      "sonarr",
			Output:       "sdk/sonarr",
			Naming:       config.PathNaming,
			PathPrefix:   "/api/v3",
			Auth:          "APIKey",
			ClientService: "service", // sdk/sonarr/service.go, written by hand
			Paging:       &config.Paging{Start: "page", Limit: "pageSize", Items: "records", Total: "totalRecords", ByPage: true},
		}},
		Workarounds: workarounds.All, // the repository's own fixes for its document
	})
}
```

It is run from the repository root, usually by make:

```bash
go run ./sdk/pandorest import      # each service's highest-versioned spec into its definitions, applying the workarounds
go run ./sdk/pandorest generate    # the packages, from those definitions
go run ./sdk/pandorest diff        # what the specs change against the checked-in definitions, breaking changes marked
go run ./sdk/pandorest check       # the definitions match the specs, every operation has a method, and how many the repository's own files name, tests left out (-uncalled lists the rest, -callers tools/ counts one directory)
go run ./sdk/pandorest resolve     # which document and definitions each service is at
```

`-service a,b` limits a command to some services, `-root` runs it against another checkout, and `diff -old <dir> -new <dir>` compares two definitions directories. `pandorest.Run` is the same thing as a function, for a test that wants to hold the repository to its own documents.

**Documents are named by version.** `<dir>/<service>-openapi-<version>.json` imports into `<dir>/<service>-<version>/` beside it, and the highest version present is the one imported and generated from, unless the service says where every version's package goes (`Outputs`): then each is imported and generated, into a package of its own that nothing ties to the others, so a caller can be on one version and import the next beside it for what it needs, the way azurerm imports two API versions of one Azure service. `-version` runs a command on one of them. The version is the release the document was taken from: for Emby and Jellyfin that is the document's own `info.version`, and for Sonarr, Radarr and Prowlarr, whose documents say `3.0.0` or `1.0.0` whatever the release, it is not, so name the file by the release. Nothing compares the two. The generated package declares both: `APIVersion` is the document's own and `DocumentVersion` the file's.

A repository that vendors its dependencies takes a new pandorest in this order: `go get` it, regenerate with `-mod=mod` (`go run -mod=mod ./sdk/pandorest generate`), then `go mod tidy` and `go mod vendor`. The other way round, the stale `vendor/` and the generated files' old imports each stop the other from building.

To refresh a spec, vendor the new document beside the old one, import it into its own definitions directory, read `diff -old <old dir> -new <new dir>`, and delete the old pair once the new one is in.

## A service

Everything about a service is in its `config.Service`, and everything but the first four is optional.

| Field | Says |
|---|---|
| `Name` | the `-service` name, and the prefix of the document and definitions names |
| `Package`, `Output` | the generated package's name and directory, for a service generated from its highest version alone |
| `Outputs` | for a service generated from every version it vendors: each document's version to its package directory (`"4.10.0.40": "sdk/emby/4.10.0.40"`); every version listed must be vendored and every document listed, and `Output` is then unused |
| `Grouped`, `Packages`, `Module`, `ClientPackage` | the grouped layout (see Generating): a package per tag, named for the tag unless `Packages` says otherwise (`"Package": "packages"`), importing one another under the repository's module path, `Module`, which is read from `go.mod` when unset |
| `Auth` | the name of the base client's authorizer: the generated `New(baseURL, token)` calls `client.<Auth>(token)`, which is `APIKey` or `Bearer` on pandorest's own |
| `ClientService` | the name of a `client.Service` written by hand in the generated package, which the generated `New` makes its client from |
| `ClientImport` | the import path of a base client of the repository's own, in place of pandorest's |
| `Dir` | where the documents and definitions live, `api-defs` unless set |
| `Naming` | `PathNaming` names a method after its HTTP method and path, `OperationIDNaming` after its operationId |
| `PathPrefix`, `Words` | for path naming: a prefix left out of names (`/api/v3`), and the spelling of run-together segments (`episodefile` as `EpisodeFile`) |
| `TagSuffix` | trimmed from tags to make group names (`Service` on Emby) |
| `Paging`, `Pagings` | the query parameters and answer properties that make an operation a list, and whether it pages by start index or by page number; a list gets a `Complete` method. `Limit` may be left empty where the server fixes the page size (TMDB), and `Items` where each answer names its list differently (Dockhand). `Pagings` are further shapes for a service whose lists do not all page alike; `import` logs every operation that takes a shape's start parameter and matches none, with why, which is how the next shape is found |
| `KeepNull` | fields, as `Schema.property`, held as pointers because their zero is not their absence: a number whose null is not 0 (season 0 is the specials), or a string a server clears only when it is sent empty, where one held by value is left out when empty |
| `WrittenWhole` | settings schemas read and written back whole, whose empty strings and zeros are sent rather than left out |
| `SendRequired` | a property the document requires is always sent |
| `PreferJSON` | a response that lists JSON beside another type answers JSON, not a file |
| `ExpectSameShape` | a status outside 2xx that answers the success shape is expected, not an error |
| `Dates`, `Days` | how the server writes a date-time and a date: `Send` is the layout a date goes out in, the one the server itself writes (`config.Dates{Send: "2006-01-02T15:04:05.0000000Z07:00"}`, `config.Dates{Send: time.DateOnly}`), and `Read` any others it is read in as well (`Read: []string{time.RFC3339, "2006-01-02T15:04:05"}` for Emby's shorter and zone-less dates). Set, every such field, option and path parameter is the package's `Time` or `Day`, a `time.Time` left out when zero, and a date no layout reads fails the decode naming it, or with `KeepUnread` is read as zero with its text kept on the value, the sweep still failing the operation. A parameter the server wants in another layout is named in the service's own file: `client.Service{SendDateAs: func(operation, parameter string) string {...}}`, asked for every date query and path parameter, `""` meaning the usual. Unset, dates stay strings |
| `IDs`, `Plain` | the kinds of id the SDK tells apart, each a bare named type over the plain int or string (`type SeriesID int`) so the compiler refuses a series id where an episode id goes: a kind names the models it is the id of (`Of`), from which its base type and every bare path `{id}` are inferred, on any method of a path whose operations answer or take such a model and on the paths under it (a delete answers nothing; the GET beside it says whose id it is), the names that always mean it (`Names`), the places inference cannot settle (`In`) or must leave alone (`Except`), and the parameters that take several as one comma-joined string (`Joined`); a foreign id is a kind by `Base`. `Shape` is a regular expression every value of the kind matches over its text (Emby's `^[0-9a-f]{32}$`), and a method refuses a path parameter or an option without it before any request is sent, with an error naming the value, where a server might answer a malformed id with a bare 500; `Example` is a value of the shape the generated tests send, required with it. `Plain` names the id-looking ones left as they are on purpose. The import logs what each kind reached and the names no kind or `Plain` covers, which is the list the setting is written from (a service that names one kind, or one `Plain` name, hears the rest; one that names neither hears nothing), and `importer.VerifyIDs` is the test a repository's file passes. `int(id)` and `SeriesID(n)` convert one value, `client.IDs` and `client.StringIDs` a list |
| `InlineSingleOptions` | an operation with one query or header parameter takes it as an argument in place of the options struct (see Generating); off, every operation with options takes the struct |
| `Credential`, `NewDoc` | the generated `New` in the service's words: what its credential is called (`API key` names the parameter `apiKey` and words the refusal of an empty one; a phrase of more than two words words the refusal and the parameter is `credential`), and its documentation |
| `Client` | the source of `client.go` in place of the default, for a service that wants a `New` of another shape; it declares no imports and may use the standard packages generated code does (`errors`, `fmt`, `strings`, `strconv`, `net/http`, `net/url`, `io`, `encoding/json`, `context`, `slices`) |
| `Notes` | a paragraph for the generated package's documentation |

A name the config gets wrong fails the import rather than doing nothing: a `KeepNull` field that is neither a string nor a nullable number, a `WrittenWhole` schema that is not there, a `Words` entry that is not its key's letters.

## Pieces

| Package | Pandora's | Does |
|---|---|---|
| `pandorest` | the commands | `Main` and `Run`: import, generate, diff, check, resolve |
| `config` | `config/resource-manager.hcl` | a service: its documents, paths, naming and the options above |
| `openapi` | the swagger parser | decodes the subset of OpenAPI 3 the documents use, mutably |
| `importer/workarounds` | `importer-rest-api-specs/components/dataworkarounds` | the interface a repository's workarounds implement, their shared helpers and the test they must pass |
| `importer` | `importer-rest-api-specs` | normalises a patched document into definitions, strictly |
| `definitions` | `api-defs/` + its models | the contract: JSON per version per tag, load, save, validate |
| `differ` | `data-api-differ` | reports operations, options, bodies, models, fields and enum values added, removed or changed |
| `generator` | `generator-go-sdk` | writes the package from definitions only |
| `sweep` | | the read sweep: every GET called on the generated SDK against a real server, for a repository's live tests |
| `client` | `go-azure-sdk/sdk/client` | the base client every generated package sends its requests through |

## Importing

`import` loads each document, applies its workarounds, and normalises it:

- **Names.** With `OperationIDNaming` a method is named after its operationId (`GetItems`; TMDB's `movie-details` is `MovieDetails`). With `PathNaming`, for operationIds that are machine-made, lossy or missing, it is named after the method and path: `GET /Items/{Id}/Similar` is `GetItemsByIdSimilar`. Schema names lose their dots and underscores (`QueryResult_BaseItemDto` is `QueryResultBaseItemDto`); fields keep the API's spelling (`Id`, `ImdbId`), and a field with a leading underscore beside its plain twin takes an `Underscore` prefix.
- **Types.** `Integer` (int), `Integer64`, `Float`, `Double`, `String`, `Boolean`, `List`, `Dictionary`, `Reference` to a model or enum, `RawObject` for JSON of no declared shape (untyped objects, unions, "binary" JSON documents), `Any`, and `RawFile` for bodies that are not JSON. An inline object becomes a model named after its owner and field; an operation's inline request and response are named after the operation.
- **Operations.** Path parameters in template order; query and header parameters as options, with lists comma-separated or one key per value as the document says, an object sent as one JSON string or, in `deepObject` style, as `name[key]=value` per key, and a header named `Accept`, `Content-Type` or `Authorization` ignored, as OpenAPI says (but for an `Accept` a workaround adds); the JSON request body, a model whether declared or inline, or raw bytes; the success response (JSON, a file, text a workaround says to read whole, or nothing); `ExpectedStatusCodes` from the 2xx responses; and `Pageable` for a list.
- **Grouping.** One definitions file per spec tag, holding the tag's operations and the models and enums only its operations use. Anything more than one tag uses is in `Common.json`. There is one Go package per service, not per tag, because shared models would otherwise tie every package to every other.

The importer is strict. An undeclared path parameter, a GET that does not say what it answers, a duplicate operationId, two operations that would make the same method name, an operation without a tag or a success response, or two schemas that would declare the same Go type fail the import with every problem listed, rather than being guessed at.

### Workarounds

A document bug is fixed with a workaround, kept in the repository that vendors the document: a type with a `Name` (`emby-search-term`), the `Service` it is for, the `Bug` it fixes in a sentence, and `Apply`, which patches the loaded document. `importer/workarounds` has the interface and the helpers they share (`Operation`, `JSONResponse`, `AddParameters`, `Property`).

`Apply` must first check the bug is there, and return an error when it is not: the parameter it adds already declared, the operation gone, the schema already carrying the field. A refreshed document that fixes the bug then fails the import, naming the workaround to delete, instead of the workaround silently doing nothing forever. `workarounds.Verify` is the test for that, one call in the repository's own tests: every workaround must apply to its vendored document, fail when applied a second time, and return rather than panic on a document that has lost its 200 responses.

#### Ready-made workarounds

The same few bugs are in most documents, so the workarounds for them are here, the default ones: a repository says which operations and why, and the patching, and the check that the bug is still there, are written once. Each is a `Workaround` like one written by hand and passes `Verify` the same way.

```go
var All = []workarounds.Workaround{
	workarounds.UndeclaredAnswers(workarounds.About{Name: "sonarr-undeclared-responses", Service: "sonarr", Bug: "fourteen GETs declare a 200 with no content, so nothing says whether they answer JSON or a file"}, map[string]workarounds.Answer{
		"GET /api/v3/filesystem":          workarounds.JSON(),
		"GET /api/v3/customformat/schema": workarounds.ListOf("CustomFormatSpecificationSchema"),
		"GET /api/v3/log/file/{filename}": workarounds.File("text/plain"),
	}),
	workarounds.WrongAnswers(workarounds.About{Name: "emby-parent-path-text", Service: "emby", Bug: "GET /Environment/ParentPath declares a JSON string; the server answers the bare path as text"}, map[string]workarounds.Correction{
		"GET /Environment/ParentPath": {Declared: workarounds.JSONString(), Answers: workarounds.Text("text/plain")},
	}),
}
```

| Workaround | The bug it is for | It fails when |
|---|---|---|
| `UndeclaredAnswers` | operations declare a success with no content, so nothing says whether they answer JSON, and in what shape, or a file | one declares its answer now |
| `WrongAnswers` | operations declare one answer and the server gives another: a JSON string sent as bare text, one record where a list is sent | one no longer declares what its correction says it does |
| `WrongStatus` | operations are documented as one status and answer another with the same body: creates that answer 201 where the document says 200 | one no longer documents the declared status, or documents the other |
| `UndeclaredParameters` | operations read a parameter the document leaves out | one declares it now |
| `UndeclaredProperties` | the server sends, and reads back, fields a schema leaves out | the schema declares one now |
| `UndeclaredDates`, `UndeclaredDays` | the server writes a date-time, or a day, in a property or parameter the document declares a plain string (Dockhand's `createdAt`); every string property of the name, wherever it sits, is declared one, for the service's `Dates` or `Days` to type. The `Except` forms leave the name alone in the operations or component schemas given, where the same name is a string of something else | no string property has the name, every one is declared a date now, or an exception excludes nothing |
| `UndeclaredEnums` | the server draws a plain string's values from a fixed set the document does not list (Dockhand's `state`, `protocol`, `mode`); each becomes a typed enum with its values as constants, named for the property unless `Name` says otherwise and everywhere unless `In` narrows it to operations or schemas, with a path inside one for a name that means two things in one answer (`"GET /api/stacks .status"` for the top-level one alone, `"GET /api/stacks containerDetails.status"` for the nested one; lists add nothing to a path), and the sweep fails an answer carrying a value not listed, printing the place in that form, a top-level place with its leading dot so that what is copied types that place alone. A map's values are walked like a list's items, adding nothing to a path. The import logs what each of these reached, `createdAt in 60 places`, so a name that reached fewer places than expected is seen there | no plain string has the name, the enum has no values, or its type name is another schema's |
| `JoinedLists` | the server reads a string parameter as several values joined by commas (Emby's `IncludeItemTypes`) where its document declares one string; a list of the enum or of strings is added beside the string as `<Field>List`, sent under the same name joined, so a caller hands two servers the same list where one takes it joined and the other repeated and no caller of the string moves; `In` narrows it to some operations | no plain string parameter has the name, the element is not an enum of the document or `string`, or an `In` matches nothing |
| `UndeclaredUnixDates` | the server writes a date as seconds or milliseconds since 1970 in a property the document declares an integer; every integer property of the name is declared one, and the package holds it as `Unix` or `UnixMilli`, read from a number and written as one | no integer property has the name, or the unit is not `seconds` or `milliseconds` |
| `NumberedPathIDs` | the document types a path parameter as text on one operation and as a number on another of the same path (Sonarr's PUT `/tag/{id}` takes a string where its GET and DELETE take an integer), which leaves a caller formatting a number it holds; the text one becomes the number the other declares, and a parameter only ever text is left alone | it reaches nothing |
| `NotAPI` | the document lists paths that are not the API's: the web interface's page, its files, its login form | one is not in the document |
| `DeclaredSchemas` | the document leaves a record out altogether (the *arr's result of testing a provider), which a workaround then refers to by name; each schema is added to the components | a name the document has |
| `AlsoAnswers` | an operation answers a body at a status the document gives none, success or not (the *arr's test of every provider: a list of results, documented as nothing, as a 200 when all pass and a 400 when any fails); each status named gets the answer, added when the document does not list it, and with `ExpectSameShape` a failure status answering the success's shape is decoded as an answer | a status declares a body already |
| `Commands` | the server queues work by name through one operation whose document declares the body as an open record (the *arr's `POST /api/v3/command`, `{"name": "RefreshSeries", "seriesId": 12}`); each command named becomes a `<Name>Command` model with its fields, a method per command that fixes the name (`RefreshSeries(ctx, input)`), an `AndWait` form that queues it and polls the queued command through the set's `Get` until its `Status` is one of `Finished`, an error for one of `Failed` or one still running when the wait ran out, and `WaitForCommand(ctx, id, wait)` for one queued the plain way; `CommandPoll` is how often it asks | an operation is not there, the queueing one takes no JSON body, no finished values, no commands, a command named like a Go keyword or with a field called `name`, a schema the document has, or a set declared twice |
| `EnumValues` | the server answers a value an enum the document declares does not list (a series status of `upcoming`); each named component schema gains the values, so the constant exists, `Known` accepts it and the sweep stops failing the answer | a schema is not there or not an enum of strings, or lists a value already |
| `OneRecord` | the document declares the same record inline wherever it appears (Dockhand's notification config, six times over as many operations), so the package has a model per place and a caller converts between them; each name becomes one component schema every named place refers to, the places named as `In` names them (`"GET /api/notifications config"`, the request and the answer both where an operation has the path on each), so the operations share one model | a name the document has a schema of, a place matching no inline object, fewer than two places, or a place whose record differs from the first's, naming the property |

`ListDeclaredAsOne("Schema")` is the commonest correction, one record declared where a list of them is sent. An answer is one of `JSON()` (no declared shape, held raw), `JSONString()`, `Model("Schema")`, `ListOf("Schema")`, `Shape(schema)` for a document that keeps its schemas with its operations, `File("media/type")` and `Text("media/type")`. A file is left for its caller to read and close. Text is short text that is not JSON, a path or a version, read whole: the method's `Model` is a `*string` holding it as it was sent, whatever the server calls it, since a server that sends bare text may label it JSON. Only a workaround makes an answer text. A log is text too, and is a file.

A workaround can also name an operation, by setting its `Name`: the method it gets in place of the one its path or operationId would make. That is how two paths that make the same name are told apart (`GET /audit` beside `GET /api/audit`, with `/api` left out of names), since the import fails on the clash rather than number the second.

A workaround can give an operation an `Accept` option too, by adding it as a header parameter (`AddParameters` with `Param{Name: "Accept", In: openapi.InHeader}`), which no document can declare for itself. It is for a server that chooses its answer by the header, where the caller has to say which it wants: asked for JSON alone, Dockhand's slow actions do the work and then answer, and asked for anything else they answer at once with a job to follow. The option is the caller's like any header option. Left empty the call asks as it would have, and set, its value is the whole header.

Workarounds are for the document's shape, what an operation takes and answers. Behaviour no document could express (a filter the server silently drops, an add it loses mid-refresh) belongs in the hand-written layer over the SDK, next to the live test that found it.

## Definitions

`<dir>/<service>-<version>/Service.json` names the package, the document's title and version, the auth and the workarounds applied; `<Group>.json` holds a tag's operations, models and constants. Operations are sorted by name, fields by JSON name, enum values and options in document order, so a spec refresh is a readable diff. The generator reads nothing else, and validates what it reads (every reference resolves, no two operations collide), so the definitions can be reviewed, and in a pinch hand-edited, without the document.

## Diffing

`diff` compares the checked-in definitions with a fresh import of each document, or two definitions directories with `-old` and `-new`, and prints what changed in API terms, for example:

```text
emby: 1 added, 0 removed, 2 changed (1 breaking)
  document: Emby Server API 4.1.1.0 -> Emby Server API 4.10.0.0
+ operation GetItemsFilters3 (GET /Items/Filters3)
~ operation GetItems (GET /Items)
    + option query searchterm: String
    ~ expected status codes [200] -> [200 204]
~ model BaseItemDto
    ~ field RunTimeTicks: Integer -> Integer64 [breaking]
```

A change is breaking when it would break a caller of the generated SDK: a removal, a changed type or name, a new request body, a status code no longer expected, a list that changes how it travels or how it pages, a number held by pointer or no longer. A reworded description or a nullable flag is reported and not breaking: the generated code is the same.

## Generating

`generate` writes a service's SDK in one of two layouts. Grouped (`Grouped: true`) is laid out the way go-azure-sdk lays out a service, as packages, so a caller names every type as `emby.X`, reaches every operation through one client, and never imports a tag's package, whose name is then free to be a word the caller uses for a variable:

```text
sdk/emby/                      package emby:  every model (model_<model>.go), every enum (constants_<tag>.go),
                                              every operation's options and response types (operation_<operation>.go),
                                              New for the base client (client.go), the base client's options and
                                              error helpers under this name (helpers.go), and the service's own service.go
sdk/emby/library/              package library: Client and NewClient (client.go), a method per operation
                                              (method_<operation>.go) and its test, the canned server (client_test.go)
sdk/emby/client/               package client: New, and Client with a field per tag (client.go)
sdk/emby/groups.json           each operation's tag and each tag's package, for a caller moving from the flat layout
```

```go
import (
	"github.com/katbyte/embyfin-mcp/sdk/emby"
	"github.com/katbyte/embyfin-mcp/sdk/emby/client"
)

c, err := client.New(url, key, emby.WithLog(log))
res, err := c.Library.GetItemsByIdAncestors(ctx, id, emby.GetItemsByIdAncestorsOptions{UserId: user})
var parent emby.BaseItemDto = res.Answer[0]
```

A package's name matches its directory, so under a version directory (`Outputs`) the types package is a directory of its own: `sdk/emby/4.10.0.40/emby`, `sdk/emby/4.10.0.40/library`, `sdk/emby/4.10.0.40/client`. A repository whose files import pandorest's own `client` beside it may name the package with `ClientPackage` (`embyclient`) to spare the alias; the types package re-exports the everyday helpers so most files never need both. A tag whose lowercased name is a Go keyword, a standard library package, `client` or the SDK's own name fails the generation and is named in `Packages`; an entry naming a tag the document lacks fails too. Method names stay unique across the SDK, so the sweep's cases and a repository's own checks go by the bare name, and `check` and the sweep walk the tag packages. An authorizer declared in the service's own file is named in `Auth` by its lowercase name (`credential`), where an uppercase one is the base client's. The packages import one another under the repository's module path, read from `go.mod` or set as `Module`, so a run against a root with no `go.mod` (a staleness test generating into a scratch directory) sets `Module`.

Moving a repository from the flat layout is mostly a script over `groups.json`: `Methods` names each method's tag, the `Complete` pagers included, and `Groups` each tag's package and the field on `Client` it is reached by, so `c.GetItems(` becomes `c.Items.GetItems(`. What the script cannot do: the hand-written `service.go` moves to the types package (`sdk/emby/4.10.0.40/emby`); a hand-written test of the whole client (one that called `New` and then a method) moves to the client package, since the types package has no methods and `New` there gives the base client; and the code that built the client by hand, once `ClientService`, `Auth` and `Credential` describe it, is unused and goes. A hand-written Go file left where a generated one goes, a `doc.go` moved beside the types say, is refused rather than overwritten.

Flat (the default) writes one package per service, with a file per operation, per model and per tag's enums:

```text
client.go                                 Client, New, APIVersion, DocumentVersion
client_test.go                            the canned servers the generated tests share
doc.go                                    package documentation, the workarounds applied
<tag>_method_<operation>.go               the method, <Name>Response,
                                          <Name>Options, and for a list
                                          <Name>Complete and <Name>CompleteResult
<tag>_method_<operation>_test.go          the operation's generated test
<tag>_model_<model>.go                    one model
<tag>_constants.go                        the tag's enums, with PossibleValuesFor<Enum>
```

Every generated file starts with the generated-code header; files with it that generation no longer produces are deleted, and anything without it is left alone, so hand-written files can live beside the generated ones, and one at a name generation writes to is refused rather than overwritten. The one exception to the header is `doc.go`, which starts with the package comment instead and says there that pandorest generated it: linters pass over a file with the header, and would tell a package with a hand-written file in it that it has no package comment. `generator.Generated` tells a generated file from one written by hand, `doc.go` included.

A method takes `ctx`, the path parameters, the body (`input`: the model by value, or an `io.Reader` and a content type), then the options struct by value, and returns `(result <Name>Response, err error)`. When the document requires none of the options the struct may be left out, so a call with no filters is `c.GetSessions(ctx)`; two structs are refused before any request is made. With `InlineSingleOptions` an operation with one option takes it as an argument instead: `c.GetMovieLookup(ctx, term)` or `c.GetMovieLookup(ctx)`, `c.PostIndexer(ctx, v, false)` sending false where `c.PostIndexer(ctx, v)` sends nothing, a required one plain, a list as its elements; an operation with two or more keeps the struct, as does a list that pages. Two things to know when moving to it: a value set only sometimes needs a slice at the call (`var force []bool; if cond { force = []bool{true} }; c.PostIndexer(ctx, v, force...)`), since the pointer that said "unset" is gone, and a required boolean or number is now always sent, where an empty struct used to leave it out. The options type is still generated, since the method builds one to send; no method takes it:

```go
res, err := c.GetItems(ctx, emby.GetItemsOptions{Recursive: new(true), Limit: new(50)})
res.HttpResponse // set whenever the server answered, its body readable again
res.Answer       // *emby.QueryResultBaseItemDto, also res.Items through the embed

all, err := c.GetItemsComplete(ctx)
all.Items        // every page
```

- Options are sent only when set: a boolean or a number is a pointer, so false, season 0 and image 0 can be asked for; an empty string or list is skipped.
- In models, booleans are `*bool` and lists and maps are `omitzero`, so a body can leave a flag to the server's default, send an explicit false, and clear a list with an empty one.
- Numbers in models are held by value, a null read as 0, except the fields `KeepNull` names, which are pointers.
- `Answer` is a pointer for a struct, enum or primitive, and the value for a list, map or raw JSON. `Answered()` says whether a body came and was decoded, false for a 204 or a call that failed first, which is the question behind every `res.Answer == nil`. A struct answer is also embedded, the same record, so `res.Answer.Name` may be read as `res.Name`, and when nothing was answered the embedded record is a zero one so that read is empty rather than a panic; `res.Record()` is the record as a value to change and write back, or an error when none was answered. The record is also there under its type's name, `res.BaseItemDto`; only the method sets it, so a response built by hand sets both or neither. A model with a field called `Answer` or `HttpResponse`, or named so, is refused for the hiding Go would do silently. An operation that answers a file has no `Answer`: its body is left unread in `HttpResponse.Body`, and the response has `WriteTo(w)`, which copies the file out and closes the body, telling a failed writer (`client.WriteError`) from a server that broke off; `Bytes(max)`, which reads it whole and closes it, failing (`client.TooLargeError`) rather than cutting short a file over `max`; `Reader()`, which hands the body over with its content type for a caller that streams it and closes it itself, an empty body when there is no answer so a deferred close is always safe; `ContentLength()`, the server's named length or -1, to refuse a file before reading it; and `Filename()`, the name the server gave it in Content-Disposition, or "", for saving it under the server's name. After any but `Reader` the body is spent. An event stream has `Reader` and `ContentLength` only, since it has no end to copy to. A failed call leaves no open body: the base client reads the refusal and closes it. An operation that expects a 204 beside its JSON answers a nil `Answer` and no error for it.
- A status outside `ExpectedStatusCodes` is a `*client.StatusError`, even a 2xx, returned with the response. A redirect an operation documents is not an error: see the base client, below.
- A `Complete` pager asks for the page size the options give (`client.DefaultPageSize` when unset) and stops on an empty or short page, or at the total when the server reports one. `Each` walks the same pages as a `range` over `iter.Seq2[Item, error]`, one result at a time, so a loop that stops asks for no page more; an error is the last pair handed out.
- A record with a `Name` and an `Id` has `GetName()` and `GetId()`, each answering its field, so code that handles records of several types takes them through one interface. `GetId` answers the field's own type, a typed id where the service declares one.
- An answer the document gives no shape to is `json.RawMessage`; `client.Decode[T](raw)` reads it into a type of the caller's own, with the package's `Time`, enum and id types reading as the generated models do, and `client.DecodeStrict[T]` fails naming a key the type has no field for.

### Generated tests

Every operation gets a test against a canned server, generated from its definition: it calls the method with a sample value for every path parameter (a slash in string ones, to prove escaping) and every option, and checks the server saw the method, the escaped path, each option on the wire, the headers, and the body and its content type. The server answers the first documented status with a sample of the documented response, which must decode (or stream, for a file); then an answer that does not decode, and a status the operation does not document, must both come back as errors with the response. A list's `Complete` must walk its pages. That exercises about 95% of a generated package without a server.

They prove the generated code does what the definitions say, not that the definitions are right about the server: that takes tests against the real thing.

### The read sweep

The generated tests cannot say whether the document is right about the server. The `sweep` package is the test that can, for a repository's live suite: it calls every GET in a service's definitions on the generated client against a real server, with arguments resolved from the suite's fixtures, and holds each answer to three things. It decodes; it carries something, because an empty list decodes into any model; and no object in it is lost whole, which is how a model whose fields do not match the server shows up.

```go
sweep.Sweep{
	Definitions: defs,   // definitions.Load of the service's directory
	Client:      sdk,    // the generated client
	StatusCode:  client.StatusCode,
	Fixtures:    sweep.Fixtures{Path: map[string]string{"Items/Id": movieID}, Always: map[string]string{"UserId": userID}},
	Cases: map[string]sweep.Case{
		"GetLiveTvTuners": {Skip: "needs a tuner"},
		"GetItemsByIdThemeMedia": {Empty: "the fixtures have no theme songs"},
	},
	MissingIDs: map[string]string{"ItemID": "00000000000000000000000000000000"},   // what the server says to an id nothing has, noted at the end
}.Run(t)
```

A path parameter's value is looked up in `Fixtures.Path` by the literal segments before it and its name, the most segments first, then by its name alone: `/api/v3/config/indexer/{id}` finds `config/indexer/id` before `indexer/id` before `id`, so the settings under `config` take another id than the list of the same name. Case does not matter.

An event stream (`text/event-stream`) never ends, so the sweep does not read one whole: it reads as far as the first event and hangs up. One that sends no event in `EventWait`, ten seconds unless set, answered with nothing, and is held to that like an empty list.

Every operation either answers with something or has a `Case` that says why not: skipped, an error status, an answer that does not decode, or an empty one, each with its reason. A case whose operation starts answering fails the sweep, so a stale one is removed, the way a stale workaround is; so does a case that names no GET. A GET the importer adds is swept on the next run with nothing to write. `Strict` also fails any key the model has no field for, for a server whose document is meant to be complete.

At the end of a run the sweep logs what it came to, `mini: 13 GET operations: 9 answered, 1 answered nothing by their Empty case, 2 refused as their case says, 1 skipped, 0 failed`, and what the answers left unproven: of the places an answer can hold an enum, a date or a boolean held by value (`Model.Field`, found through the answer types), which ones a value came for, and per enum which listed values no answer carried. A boolean held by value reads false whether the server left it out or sent false, and an enum value no answer carried may be one the server never sends, so these are notes, not failures: `enum ContainerState: 4 places in what a GET answers, a value seen at 3 (never: Stack.State); listed values never seen: dead, removing`; a place is a field of a record a GET answers, so a model no GET answers is not counted. `sweep.NeverCalled(defs, calls.Seen(), excused)` is the check for a whole live suite rather than the sweep's GETs: with the suite's client made `client.WithCalls(&calls)`, every request it sent is recorded, and at the end each operation none reached must be excused by name with a reason, so the suite fails on one neither called nor excused and on an excuse that names no operation. `MissingIDs` gives, per kind of id, a value of the right shape nothing has, and the sweep calls the first GET whose path takes the kind with it and notes what came back, `a missing ItemID answers HTTP 404 (GET /Items/{id})`, so the service's own file can say it rather than each tool learning it from the server.

### The base client

Generated code sends every request through the `client` package, after go-azure-sdk's `sdk/client`. It is built on [go-kt](https://github.com/katbyte/go-kt)'s `chttp`: a read is asked again for a dropped connection or a gateway's answer and a write is sent once, an answer is read whole up to a limit, and every exchange is traced to a logger when one is handed in, with the credentials hidden.

A call asks for what its operation is documented to answer. One that answers JSON, or nothing, sends `Accept: application/json`. One that answers a file or an event stream asks for that type first and JSON after it (`Accept: text/event-stream, application/json;q=0.9`), because some servers choose their answer by the header: asked for JSON alone, they send JSON where the document says a stream. JSON stays in the header so that a refusal can still be written as JSON. Where the choice is the caller's to make, call by call, a workaround gives the operation an `Accept` option (see Workarounds).

What is common to every service is in the package. What is one service's own about being talked to is a `client.Service`, which the repository writes by hand in a file beside the generated ones and names in its config (`ClientService`). The file is the repository's: it has no generated header, so `generate` never writes it or removes it. It needs no exceptions from a linter: the package comment it would be asked for is in `doc.go`, and `Example` is a host (`nas:7878`, shown as `http://nas:7878`) or, for a hosted API, a whole address.

```go
// sdk/radarr/service.go
package radarr

// service is what is Radarr's own about being talked to.
var service = client.Service{
	Name:           "Radarr",
	UserAgent:      "radarr-mcp/" + version.Version,
	Example:        "nas:7878",
	RedirectAdvice: "set the server url to the address Radarr itself answers on, with its URL base if it has one",
	Messages:       errorMessages, // what Radarr said went wrong, read out of its error body
	Note:           note,          // what a 401 means here, and a 404 that is not JSON
}
```

| A service says | Without it |
|---|---|
| `Name`, `UserAgent`, `Example` | "the server", Go's user agent, no example address beside a refused url |
| `SecretHeaders`, `SecretNames` | `Authorization` and `X-Api-Key` are hidden from a trace, and what chttp hides unasked |
| `FollowRedirects`, `RedirectAdvice`, `WebPageAdvice` | a redirect no operation documents is refused, since following one turns a write into a read that reports success |
| `Timeout`, `HeaderWait`, `MaxResponse` | two minutes for a request, all of it for the server to start answering, 64 MiB held |
| `Retry` | chttp's rule: a read, three times, for a dropped connection or a 502, 503 or 504 |
| `Messages`, `Note` | an error shows the start of what the server sent |

The generated `New(baseURL, token, opts...)` wraps the token in the authorizer `Auth` names (`client.APIKey` for a key in `X-Api-Key`, `client.Bearer` for a bearer token) and passes the options on: `client.WithLog`, `WithTransport`, `WithRetry` and `WithCookies` are what the application using the SDK decides. A service that calls its credential something else says so in its config (`Credential: "API key"` makes it `New(baseURL, apiKey, opts...)`, refusing an empty one with "API key is required"), and words New's documentation with `NewDoc`. One that takes its credential in a way of its own writes the authorizer in the same file and gives its config a `Client` source whose `New` uses it.

What happens to a redirect is the document's to say, operation by operation, so a workaround can settle any one of them:

| The operation documents | A redirect is |
|---|---|
| no redirect | refused with the service's advice, or followed if the service sets `FollowRedirects`: it nearly always means the server url points at something in front of the server |
| a redirect and no 2xx (a login that sends its caller on) | the answer: handed back unfollowed, to read where it points and the cookie it set |
| a redirect beside a 2xx | on the way to the answer: followed, the credentials staying on the server's host |

An error for an undocumented status is a `*client.StatusError`: chttp's, which it unwraps to, with the server's words kept apart as `Messages` as well as joined in its text. `client.StatusCode` and `Messages` read one out of an error, and `IsNotFound`, `IsUnauthorized`, `IsForbidden`, `IsBadRequest` (400 or 422), `IsConflict`, `IsTooManyRequests` and `IsServerError` (any 5xx) say which it is; the types package offers each under its own name.

A value the client must never repeat is kept out of every error it makes: the credential its authorizer holds, and any more named with `client.WithSecrets`, read `***` wherever they would appear in a status error's body, its messages, a refused request's path or a decode failure's preview; a value shorter than six characters is left as it is, since hiding it would hide pieces of ordinary words. The generated `TestNew` proves it on a refusal that echoes the token. A service that answers 200 to everything and says in the body whether the call worked declares `Outcome` in its own file, the field, the values that mean failure with what each means, and the field with the server's words, and such a call returns a `*client.OutcomeError` with the answer still decoded; `client.OutcomeValue(err)` is the value to branch on, and a service's file can name a helper per value.

`Client.HTTPClient` is there for the generated tests, which put their canned server's own client in it. An application that wants to see or shape the traffic passes `client.WithTransport` instead, and keeps the retries and the trace.

A repository can still keep a base client of its own and name it in `ClientImport`. Generated code calls `New(baseURL, authorizer, ...Option)`, `RequestOptions` (with `Accept` for an operation that answers a file), `NewRequest`, `Marshal`, `SetBody`, `Execute`, `Response.Unmarshal`, `Headers` and `QueryParams` with `Append`, `CSV`, `JSONObject`, `DeepObject`, `DefaultPageSize` and `StatusCode`, and its tests set `HTTPClient` and read `BaseURL`.

## What was left out of Pandora

Resource ID types and parsers (the servers' ids are opaque strings), per-model predicates for `CompleteMatchingPredicate`, `Default<Name>Options` constructors (the zero value is the default), long-running operation pollers, API versions, the Terraform and documentation generators, and the data API server: a handful of documents in one repository need none of it.

## Development

```bash
make tools      # build the pinned dev tools into .tools/bin
make check-all  # build + test + lint + actionlint + yamllint + shellcheck + typos + depscheck
make cover      # tests with coverage; the coverage workflow publishes the total as the README badge
```

Nothing is vendored; the only dependency is go-kt, for the HTTP client the base client is built on. Tool versions are pinned in `.tools/go.mod` and dependabot keeps them current.
