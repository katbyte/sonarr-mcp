# pandorest

[![GitHub release](https://img.shields.io/github/v/release/katbyte/pandorest?color=blueviolet)](https://github.com/katbyte/pandorest/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/pandorest?color=00ADD8)](https://github.com/katbyte/pandorest/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/pandorest?color=blue)](https://github.com/katbyte/pandorest/blob/main/LICENSE)
![build](https://github.com/katbyte/pandorest/actions/workflows/build.yaml/badge.svg)
![test](https://github.com/katbyte/pandorest/actions/workflows/pr-tests.yaml/badge.svg)
![lint](https://github.com/katbyte/pandorest/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/pandorest/badges/coverage.json)](https://github.com/katbyte/pandorest/actions/workflows/coverage.yaml)

pandorest generates typed Go SDKs from vendored OpenAPI documents. It was written in embyfin-mcp for the Emby, Jellyfin and TMDB SDKs, copied into three more repositories, and is now here so they share one.

Inspired by [Pandora](https://github.com/hashicorp/pandora), the [go-azure-sdk](https://github.com/hashicorp/go-azure-sdk) generator: the same importer, definitions, differ and generator pipeline, scaled down to a handful of documents in one repository:

```
api-defs/
  emby-openapi-4.10.0.40.json ──┐               ┌─ emby-4.10.0.40/*.json ──┐
  jellyfin-openapi-12.1.0.json ─┼─ import ──────┼─ jellyfin-12.1.0/*.json ─┼─ generate ─ sdk/emby, sdk/jf, sdk/tmdb
  tmdb-openapi-2026.09.22.json ─┘ (workarounds) └─ tmdb-2026.09.22/*.json ─┘
                                                        │
                                         diff ──────────┘  (what a refreshed spec changes)
```

It does what the alternatives were tried and rejected for (oapi-codegen, openapi-generator): errors for undocumented statuses rather than a result per content type, plain string ids, readable names for machine-made operationIds, no builder chains or nullable wrappers, and a test for every operation it writes.

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
go run ./sdk/pandorest check       # the definitions match the specs, and every operation has a method
go run ./sdk/pandorest resolve     # which document and definitions each service is at
```

`-service a,b` limits a command to some services, `-root` runs it against another checkout, and `diff -old <dir> -new <dir>` compares two definitions directories. `pandorest.Run` is the same thing as a function, for a test that wants to hold the repository to its own documents.

**Documents are named by version.** `<dir>/<service>-openapi-<version>.json` imports into `<dir>/<service>-<version>/` beside it, and the highest version present is the one imported and generated from. The version is the release the document was taken from: for Emby and Jellyfin that is the document's own `info.version`, and for Sonarr, Radarr and Prowlarr, whose documents say `3.0.0` or `1.0.0` whatever the release, it is not, so name the file by the release. Nothing compares the two. The generated package declares both: `APIVersion` is the document's own and `DocumentVersion` the file's.

A repository that vendors its dependencies takes a new pandorest in this order: `go get` it, regenerate with `-mod=mod` (`go run -mod=mod ./sdk/pandorest generate`), then `go mod tidy` and `go mod vendor`. The other way round, the stale `vendor/` and the generated files' old imports each stop the other from building.

To refresh a spec, vendor the new document beside the old one, import it into its own definitions directory, read `diff -old <old dir> -new <new dir>`, and delete the old pair once the new one is in.

## A service

Everything about a service is in its `config.Service`, and everything but the first four is optional.

| Field | Says |
|---|---|
| `Name` | the `-service` name, and the prefix of the document and definitions names |
| `Package`, `Output` | the generated package's name and directory |
| `Auth` | the name of the base client's authorizer: the generated `New(baseURL, token)` calls `client.<Auth>(token)`, which is `APIKey` or `Bearer` on pandorest's own |
| `ClientService` | the name of a `client.Service` written by hand in the generated package, which the generated `New` makes its client from |
| `ClientImport` | the import path of a base client of the repository's own, in place of pandorest's |
| `Dir` | where the documents and definitions live, `api-defs` unless set |
| `Naming` | `PathNaming` names a method after its HTTP method and path, `OperationIDNaming` after its operationId |
| `PathPrefix`, `Words` | for path naming: a prefix left out of names (`/api/v3`), and the spelling of run-together segments (`episodefile` as `EpisodeFile`) |
| `TagSuffix` | trimmed from tags to make group names (`Service` on Emby) |
| `Paging` | the two query parameters and two answer properties that make an operation a list, and whether it pages by start index or by page number; a list gets a `Complete` method |
| `KeepNull` | number fields, as `Schema.property`, held as pointers because their null is not their zero (season 0 is the specials) |
| `WrittenWhole` | settings schemas read and written back whole, whose empty strings and zeros are sent rather than left out |
| `SendRequired` | a property the document requires is always sent |
| `PreferJSON` | a response that lists JSON beside another type answers JSON, not a file |
| `ExpectSameShape` | a status outside 2xx that answers the success shape is expected, not an error |
| `Credential`, `NewDoc` | the generated `New` in the service's words: what its credential is called (`API key` names the parameter `apiKey` and words the refusal of an empty one), and its documentation |
| `Client` | the source of `client.go` in place of the default, for a service that wants a `New` of another shape; it declares no imports and may use the standard packages generated code does (`errors`, `fmt`, `strings`, `strconv`, `net/http`, `net/url`, `io`, `encoding/json`, `context`, `slices`) |
| `Notes` | a paragraph for the generated package's documentation |

A name the config gets wrong fails the import rather than doing nothing: a `KeepNull` field that is not a nullable number, a `WrittenWhole` schema that is not there, a `Words` entry that is not its key's letters.

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
- **Operations.** Path parameters in template order; query and header parameters as options, with lists comma-separated or one key per value as the document says, an object sent as one JSON string or, in `deepObject` style, as `name[key]=value` per key, and a header named `Accept`, `Content-Type` or `Authorization` ignored, as OpenAPI says; the JSON request body, a model whether declared or inline, or raw bytes; the success response (JSON, a file, or nothing); `ExpectedStatusCodes` from the 2xx responses; and `Pageable` for a list.
- **Grouping.** One definitions file per spec tag, holding the tag's operations and the models and enums only its operations use. Anything more than one tag uses is in `Common.json`. There is one Go package per service, not per tag, because shared models would otherwise tie every package to every other.

The importer is strict. An undeclared path parameter, a GET that does not say what it answers, a duplicate operationId, two operations that would make the same method name, an operation without a tag or a success response, or two schemas that would declare the same Go type fail the import with every problem listed, rather than being guessed at.

### Workarounds

A document bug is fixed with a workaround, kept in the repository that vendors the document: a type with a `Name` (`emby-search-term`), the `Service` it is for, the `Bug` it fixes in a sentence, and `Apply`, which patches the loaded document. `importer/workarounds` has the interface and the helpers they share (`Operation`, `JSONResponse`, `AddParameters`, `Property`).

`Apply` must first check the bug is there, and return an error when it is not: the parameter it adds already declared, the operation gone, the schema already carrying the field. A refreshed document that fixes the bug then fails the import, naming the workaround to delete, instead of the workaround silently doing nothing forever. `workarounds.Verify` is the test for that, one call in the repository's own tests: every workaround must apply to its vendored document, fail when applied a second time, and return rather than panic on a document that has lost its 200 responses.

A workaround can also name an operation, by setting its `Name`: the method it gets in place of the one its path or operationId would make. That is how two paths that make the same name are told apart (`GET /audit` beside `GET /api/audit`, with `/api` left out of names), since the import fails on the clash rather than number the second.

Workarounds are for the document's shape, what an operation takes and answers. Behaviour no document could express (a filter the server silently drops, an add it loses mid-refresh) belongs in the hand-written layer over the SDK, next to the live test that found it.

## Definitions

`<dir>/<service>-<version>/Service.json` names the package, the document's title and version, the auth and the workarounds applied; `<Group>.json` holds a tag's operations, models and constants. Operations are sorted by name, fields by JSON name, enum values and options in document order, so a spec refresh is a readable diff. The generator reads nothing else, and validates what it reads (every reference resolves, no two operations collide), so the definitions can be reviewed, and in a pinch hand-edited, without the document.

## Diffing

`diff` compares the checked-in definitions with a fresh import of each document, or two definitions directories with `-old` and `-new`, and prints what changed in API terms, for example:

```
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

`generate` writes one package per service, with a file per operation, per model and per tag's enums:

```
client.go                                 Client, New, APIVersion, DocumentVersion
client_test.go                            the canned servers the generated tests share
doc.go                                    package documentation, the workarounds applied
<tag>_method_<operation>.go               the method, <Name>OperationResponse,
                                          <Name>OperationOptions, and for a list
                                          <Name>Complete and <Name>CompleteResult
<tag>_method_<operation>_test.go          the operation's generated test
<tag>_model_<model>.go                    one model
<tag>_constants.go                        the tag's enums, with PossibleValuesFor<Enum>
```

Every generated file starts with the generated-code header; files with it that generation no longer produces are deleted, and anything without it is left alone, so hand-written files can live beside the generated ones. The one exception is `doc.go`, which starts with the package comment instead and says there that pandorest generated it: linters pass over a file with the header, and would tell a package with a hand-written file in it that it has no package comment. `generator.Generated` tells a generated file from one written by hand, `doc.go` included.

A method takes `ctx`, the path parameters, the body (`input`: the model by value, or an `io.Reader` and a content type), then the options struct by value, and returns `(result <Name>OperationResponse, err error)`:

```go
res, err := c.GetItems(ctx, emby.GetItemsOperationOptions{Recursive: new(true), Limit: new(50)})
res.HttpResponse // set whenever the server answered, its body readable again
res.Model        // *emby.QueryResultBaseItemDto

all, err := c.GetItemsComplete(ctx, emby.GetItemsOperationOptions{Recursive: new(true)})
all.Items        // every page
```

- Options are sent only when set: a boolean or a number is a pointer, so false, season 0 and image 0 can be asked for; an empty string or list is skipped.
- In models, booleans are `*bool` and lists and maps are `omitzero`, so a body can leave a flag to the server's default, send an explicit false, and clear a list with an empty one.
- Numbers in models are held by value, a null read as 0, except the fields `KeepNull` names, which are pointers.
- `Model` is a pointer for a struct, enum or primitive, and the value for a list, map or raw JSON. An operation that answers a file has no `Model`: its body is left unread in `HttpResponse.Body` for the caller to read and close. An operation that expects a 204 beside its JSON answers a nil `Model` and no error for it.
- A status outside `ExpectedStatusCodes` is a `*client.StatusError`, even a 2xx, returned with the response. A redirect an operation documents is not an error: see the base client, below.
- A `Complete` pager asks for the page size the options give (`client.DefaultPageSize` when unset) and stops on an empty or short page, or at the total when the server reports one.

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
}.Run(t)
```

A path parameter's value is looked up in `Fixtures.Path` by the literal segments before it and its name, the most segments first, then by its name alone: `/api/v3/config/indexer/{id}` finds `config/indexer/id` before `indexer/id` before `id`, so the settings under `config` take another id than the list of the same name. Case does not matter.

Every operation either answers with something or has a `Case` that says why not: skipped, an error status, an answer that does not decode, or an empty one, each with its reason. A case whose operation starts answering fails the sweep, so a stale one is removed, the way a stale workaround is; so does a case that names no GET. A GET the importer adds is swept on the next run with nothing to write. `Strict` also fails any key the model has no field for, for a server whose document is meant to be complete.

### The base client

Generated code sends every request through the `client` package, after go-azure-sdk's `sdk/client`. It is built on [go-kt](https://github.com/katbyte/go-kt)'s `chttp`: a read is asked again for a dropped connection or a gateway's answer and a write is sent once, an answer is read whole up to a limit, and every exchange is traced to a logger when one is handed in, with the credentials hidden.

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

An error for an undocumented status is a `*client.StatusError`: chttp's, which it unwraps to, with the server's words kept apart as `Messages` as well as joined in its text. `client.StatusCode`, `IsNotFound`, `IsUnauthorized`, `IsForbidden` and `Messages` read one out of an error.

`Client.HTTPClient` is there for the generated tests, which put their canned server's own client in it. An application that wants to see or shape the traffic passes `client.WithTransport` instead, and keeps the retries and the trace.

A repository can still keep a base client of its own and name it in `ClientImport`. Generated code calls `New(baseURL, authorizer, ...Option)`, `RequestOptions`, `NewRequest`, `Marshal`, `SetBody`, `Execute`, `Response.Unmarshal`, `Headers` and `QueryParams` with `Append`, `CSV`, `JSONObject`, `DeepObject`, `DefaultPageSize` and `StatusCode`, and its tests set `HTTPClient` and read `BaseURL`.

## What was left out of Pandora

Resource ID types and parsers (the servers' ids are opaque strings), per-model predicates for `CompleteMatchingPredicate`, `Default<Name>OperationOptions` constructors (the zero value is the default), long-running operation pollers, API versions, the Terraform and documentation generators, and the data API server: a handful of documents in one repository need none of it.

## Development

```bash
make tools      # build the pinned dev tools into .tools/bin
make check-all  # build + test + lint + actionlint + yamllint + shellcheck + typos + depscheck
make cover      # tests with coverage; the coverage workflow publishes the total as the README badge
```

Nothing is vendored; the only dependency is go-kt, for the HTTP client the base client is built on. Tool versions are pinned in `.tools/go.mod` and dependabot keeps them current.
