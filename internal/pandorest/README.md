# pandorest

pandorest generates the Sonarr SDK, `lib/sonarr`, from the OpenAPI document in
Sonarr's own source tree.

Inspired by [Pandora](https://github.com/hashicorp/pandora), the
[go-azure-sdk](https://github.com/hashicorp/go-azure-sdk) generator: the same
importer, definitions, differ and generator pipeline, scaled down to one
document. It is the generator embyfin-mcp uses for its Emby and Jellyfin SDKs,
taught Sonarr's paging and authentication.

```
docs/sonarr-openapi.json ── import ── api-definitions/sonarr/*.json ── generate ── lib/sonarr
                         (workarounds)          │
                                  diff ─────────┘  (what a refreshed spec changes)
```

```bash
make generate          # import + generate
make pandorest-diff    # what docs/sonarr-openapi.json changes against api-definitions/, breaking changes marked
make apicheck          # the definitions match the spec, and every operation has a method
make gencheck          # regenerate, fail if anything is uncommitted
```

It does what the alternatives were tried and rejected for (oapi-codegen,
openapi-generator): errors for undocumented statuses rather than a result per
content type, readable names for a document with no operationIds, no builder
chains or nullable wrappers, and a generated test for every operation.

## Pieces

| Package | Pandora's | Does |
|---|---|---|
| `config` | `config/resource-manager.hcl` | the service: spec, definitions and output paths, naming, authorizer |
| `openapi` | the swagger parser | decodes the subset of OpenAPI 3 the document uses, mutably |
| `importer/workarounds` | `importer-rest-api-specs/components/dataworkarounds` | one named fix per document bug |
| `importer` | `importer-rest-api-specs` | normalises a patched document into definitions, strictly |
| `definitions` | `api-definitions/` + its models | the contract: JSON per tag, load, save, validate |
| `differ` | `data-api-differ` | reports operations, options, bodies, models, fields and enum values added, removed or changed |
| `generator` | `generator-go-sdk` | writes the package from definitions only |
| `lib/client` (outside) | `go-azure-sdk/sdk/client` | the hand-written base client the SDK sends through |

## Importing

`import` loads the document, applies its workarounds, and normalises it:

- **Names.** Sonarr's document has no operationIds, so methods are named
  after the method and path, with the `/api/v3` prefix left off and the
  run-together words of the paths spelled out (`config.Service.Words`):
  `GET /api/v3/series/{id}` is `GetSeriesById`, `DELETE
  /api/v3/episodefile/bulk` is `DeleteEpisodeFileBulk`, and `GET /ping`,
  outside the prefix, is `GetPing`. Schema names lose their dots and
  underscores; fields keep the API's spelling (`TvdbId`, `QualityProfileId`).
- **Types.** `Integer` (int), `Integer64`, `Float`, `Double`, `String`,
  `Boolean`, `List`, `Dictionary`, `Reference` to a model or enum, `RawObject`
  for JSON of no declared shape, `Any`, and `RawFile` for bodies that are not
  JSON. An inline object becomes a model named after its owner and field.
- **Operations.** Path parameters in template order; query and header
  parameters as options, with lists comma-separated or one key per value as
  the document says (Sonarr's are all one key per value); the JSON request
  body or raw bytes; the success response (JSON, a file, or nothing);
  `ExpectedStatusCodes` from the 2xx responses; and `Pageable` for a GET with
  `page` and `pageSize` that answers `records` and `totalRecords` (Sonarr's
  `PagingResource`).
- **Grouping.** One definitions file per spec tag, holding the tag's
  operations and the models and enums only its operations use. Anything more
  than one tag uses, `SeriesResource` above all, is in `Common.json`. There is
  one Go package, not one per tag, because those shared models would
  otherwise tie every package to every other.

The importer is strict. An undeclared path parameter, a GET that does not say
what it answers, a duplicate operationId, an operation without a tag or a
success response, or two schemas that would declare the same Go type fail the
import with every problem listed, rather than being guessed at.

### Workarounds

A document bug is fixed with a workaround in `importer/workarounds`: a type
with a `Name` (`sonarr-created-accepted`), the `Service` it is for, the `Bug`
it fixes in a sentence, and `Apply`, which patches the loaded document. Add it
to `All`.

`Apply` must first check the bug is there, and return an error when it is not:
the response it declares already declared, the operation gone, the schema
already in the document. A refreshed document that fixes the bug then fails
the import, naming the workaround to delete, instead of the workaround
silently doing nothing forever. The unit tests enforce this: every workaround
must apply to the vendored document and must fail when applied a second time.

Sonarr's document is written by Swashbuckle from the controllers, which
describes the request side well and the answers poorly, so the workarounds
are about answers: every operation is documented as a 200 where the creates
answer 201 and the updates 202, and an action returning `object` or
`IActionResult` is documented as answering nothing. Each table in
`sonarr.go` was read off the controllers in the release's source and is
proved by the integration suite against the same release.

Workarounds are for the document's shape - what an operation takes and
answers. Behaviour no document could express (a command that queues rather
than runs, a list that ignores a filter) belongs in the tools, next to the
live test that found it.

## Definitions

`api-definitions/sonarr/Service.json` names the package, the document's title
and version, the authorizer and the workarounds applied; `<Group>.json` holds
a tag's operations, models and constants. Operations are sorted by name,
fields by JSON name, enum values and options in document order, so a spec
refresh is a readable diff. The generator reads nothing else, and validates
what it reads (every reference resolves, no two operations collide), so the
definitions can be reviewed, and in a pinch hand-edited, without the document.

## Diffing

`diff` compares the checked-in definitions with a fresh import of the
document, or two definitions directories with `-old` and `-new`, and prints
what changed in API terms, for example:

```
sonarr: 1 added, 0 removed, 2 changed (1 breaking)
  document: Sonarr 3.0.0 -> Sonarr 3.0.0
+ operation GetSeriesByIdStatistics (GET /api/v3/series/{id}/statistics)
~ operation GetQueue (GET /api/v3/queue)
    + option query includeSeries: Boolean
    ~ expected status codes [200] -> [200 204]
~ model SeriesResource
    ~ field Runtime: Integer -> Integer64 [breaking]
```

A change is breaking when it would break a caller of the generated SDK: a
removal, a changed type or name, a new request body, a status code no longer
expected. `-exit-code` exits 1 when anything differs.

## Generating

`generate` writes one package, with a file per operation, per model and per
tag's enums:

```
client.go                                 Client, New
client_test.go                            the canned server the generated tests share
doc.go                                    package documentation, the workarounds applied
<tag>_method_<operation>.go               the method, <Name>OperationResponse,
                                          <Name>OperationOptions, and for a paged
                                          list <Name>Complete and <Name>CompleteResult
<tag>_method_<operation>_test.go          the operation's generated test
<tag>_model_<model>.go                    one model
<tag>_constants.go                        the tag's enums, with PossibleValuesFor<Enum>
```

Every generated file starts with the generated-code header; files with it that
generation no longer produces are deleted, and anything without it is left
alone.

### Generated tests

Every operation gets a test against a canned server, generated from its
definition: it calls the method with a sample value for every path parameter
(a slash in string ones, to prove escaping) and every option, and checks the
server saw the method, the escaped path, each option on the wire (lists
comma-separated or one key per value, as defined), the headers, and the body
and its content type. The server answers the first documented status with a
sample of the documented response, which must decode (or stream, for a file);
then an answer that does not decode, and a status the operation does not
document, must both come back as errors with the response. A paged list's
`Complete` must walk two pages. That exercises most of the generated package
without a server.

They prove the generated code does what the definitions say, not that the
definitions are right about the server: the integration suite does that, with
a read sweep over every GET and bespoke tests on real data.

A method takes `ctx`, the path parameters, the body (`input`: the model by
value, or an `io.Reader` and a content type), then the options struct by value,
and returns `(result <Name>OperationResponse, err error)`:

```go
res, err := c.GetSeriesById(ctx, 7, sonarr.GetSeriesByIdOperationOptions{IncludeSeasonImages: new(true)})
res.HttpResponse // set whenever the server answered, its body readable again
res.Model        // *sonarr.SeriesResource

all, err := c.GetWantedMissingComplete(ctx, sonarr.GetWantedMissingOperationOptions{Monitored: new(true)})
all.Items        // every page's records
```

- Options are sent only when set: zero values are skipped, booleans are
  `*bool`.
- In models, booleans are `*bool` and lists and maps are `omitzero`, so a body
  can leave a flag to the server's default, send an explicit false, and clear a
  list with an empty one.
- A model the config names as written whole (`config.Service.WrittenWhole`:
  Sonarr's settings sections, read with a GET and saved with a PUT of all of
  it) always sends its strings and numbers, empty or not. Sonarr saves a
  section field by field, and a field left out arrives as null: the host
  settings answer 500 to one, and the others keep the old value instead of
  the `""` a caller cleared it to.
- `Model` is a pointer for a struct, enum or primitive, and the value for a
  list, map or raw JSON. An operation that answers a file has no `Model`: its
  body is left unread in `HttpResponse.Body` for the caller to read and close.
- A status outside `ExpectedStatusCodes` is a `*client.StatusError`, even a
  2xx, returned with the response; its message carries Sonarr's own
  validation failures or error message. `client.IsNotFound(err)` and
  `client.WasNotFound(res.HttpResponse)` recognise a 404.
- A `Complete` pager asks for `pageSize` records a page
  (`client.DefaultPageSize` when unset), from page 1 unless the options say
  otherwise, and stops on an empty or short page, or at `totalRecords`.

## What was left out of Pandora

Resource ID types and parsers (Sonarr's ids are plain integers), per-model
predicates for `CompleteMatchingPredicate`, `Default<Name>OperationOptions`
constructors (the zero value is the default), long-running operation pollers
(a Sonarr command is polled by the tools, which know what they are waiting
for), API versions, the Terraform and documentation generators, and the data
API server: one document in one repository needs none of it.
