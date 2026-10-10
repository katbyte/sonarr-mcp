## v0.6.0 (2026-10-10)

- add ready-made workarounds to `importer/workarounds`, for the bugs most documents have and each repository patched by hand: `UndeclaredAnswers` (a success with no content), `WrongAnswers` (one answer declared, another given), `WrongStatus` (a create documented as 200 that answers 201), `UndeclaredParameters`, `UndeclaredProperties` (fields the server sends that a schema leaves out) and `NotAPI` (the web interface's paths). A repository gives an `About` (name, service, bug) and says which operations; the patching and the check that the bug is still in the document are here. An answer is said with `JSON`, `JSONString`, `Model`, `ListOf`, `Shape`, `File` or `Text`
- `workarounds.Verify` says a workaround "does not apply to the vendored document, so the bug is not there or the workaround is written wrong", where it said only that the bug was not there: a schema name misspelt in a workaround's own table read as the bug having gone
- add text as a kind of answer: short text that is not JSON (a path, a version) is read whole into the method's `Model`, a `*string`, as it was sent and whatever the server calls it, where it was a file its caller had to read and close. Only a workaround asks for it (`workarounds.Text`), since a log is text too; nothing moves for an operation that does not. A base client of a repository's own needs `Response.Text` if it has one
- `openapi.JSONMedia` and `openapi.PickJSON` are the importer's rules for which media types are JSON, for a workaround to share

## v0.5.0 (2026-10-10)

- a workaround can give an operation an `Accept` option, for a server that chooses its answer by the header where the choice is the caller's to make call by call (Dockhand's slow actions wait and answer how it went when asked for JSON alone, and answer at once with a job to follow when asked for anything else). The importer keeps a header parameter named `Accept` that a workaround adds (`AddParameters` with `Param{Name: "Accept", In: openapi.InHeader}`), where it ignores a document's own as OpenAPI says to; the option is generated like any header option, and when set its value is the whole header. Nothing moves for a repository that adds none

## v0.4.0 (2026-10-10)

- a call asks for what its operation is documented to answer: one that answers a file or an event stream sends that type first in `Accept` with JSON after it (`text/event-stream, application/json;q=0.9`), where every call asked for `application/json` alone. A server that chooses its answer by the header (Dockhand's dashboard stream answers JSON to a caller that asks for JSON alone) now sends what its document says. Calls that answer JSON, or nothing, are unchanged
- generator (breaking for a base client of a repository's own): a method that answers a file sets `Accept` in its `RequestOptions`, so such a client needs the field; a regenerated package gains one line in each such method and, in its test, the check of the header (three lines, and the `strings` import where the test had none)
- sweep: an event stream (`text/event-stream`, by its document or by what the server answers) is read as far as its first event and then hung up on, where it was read like a file and ran out of time, so that each one needed a `Skip`. One that sends no event in `EventWait` (ten seconds unless set), or ends without one, is an answer with nothing in it and takes an `Empty` or `MayBeEmpty` case like an empty list; one that answers another content type than the event stream its document says, or breaks off part way, fails

## v0.3.2 (2026-10-10)

- sweep: a path parameter's fixture is looked up by as many of the literal segments before it as there are, the most of them first, where it was only ever the one: `config/indexer/id` is found before `indexer/id`, so the settings under `/config/indexer/{id}` can be given another id than the indexers at `/indexer/{id}` with no case for it. A fixture keyed by one segment or by the name alone is found as before

## v0.3.1 (2026-10-10)

- a service words the generated `New` without copying it: `Credential` is what it calls its credential ("API key" names the parameter `apiKey` and refuses an empty one with "API key is required"), and `NewDoc` is New's documentation; a `Client` source is now only for a `New` of another shape
- the generated `doc.go` no longer starts with the generated header, so that a linter sees the package comment in it: a file written by hand beside the generated ones (the service's own) was otherwise told its package has none, and every repository carried an exception for it. `generator.Generated` still knows `doc.go` as generated; a test that tells generated files by `generator.Header` must use it instead
- `client.Service.Example` can be a host alone (`nas:7878`, shown as `http://nas:7878`), so a service's file holds no `http://` address for a linter to object to

## v0.3.0 (2026-10-10)

- add `client`: the base client every generated package sends its requests through, one copy in place of the one each repository kept, built on go-kt's `chttp`; what is a service's own (its name, how long it may take, what its refusals mean, how its error body reads) is a `client.Service` the repository writes by hand beside its generated package and names with `ClientService`
- generator (breaking): the default `client.go` is built on `client` unless `ClientImport` names another, and its `New` takes the base client's options after the token, so a base client of a repository's own needs an `Option` type and a `New` that takes them
- a status error keeps what the server said as a list (`StatusError.Messages`, `client.Messages`) as well as joined in its text
- what happens to a redirect is the document's to say, per operation: one that documents a redirect and no 2xx answers the redirect, handed back unfollowed (it used to fail the import); one that documents a redirect beside a 2xx expects both and has the redirect followed to its answer; a redirect no operation documents is refused or followed as the service says
- `internal/client`, the stand-in the tests built generated packages against, is gone: they build against `client`

## v0.2.0 (2026-10-10)

- add `sweep`: the read sweep a repository runs against a real server, every GET in a service's definitions called on the generated client and its answer held to decoding, carrying something and losing no object whole; one package made of the copies in embyfin-mcp, sonarr-mcp, radarr-mcp and prowlarr-mcp, with radarr's check for undeclared fields as `Strict` and prowlarr's `Sometimes`
- importer (breaking): two operations that make the same method name fail the import, where the second used to be numbered (`GetAudit2`) with a warning; which one took the number followed the order of the paths, so a refreshed document could rename a method
- a workaround can name an operation (`Name` on `openapi.Operation`), which is how such a clash is settled

## v0.1.0 (2026-10-09)

- initial release: the SDK generator extracted from embyfin-mcp's `sdk/pandorest`, where it writes the Emby, Jellyfin and TMDB SDKs, so the copies in sonarr-mcp, radarr-mcp and prowlarr-mcp can be replaced by one
- a repository hands its services and its workarounds to `pandorest.Main` from a main of a few lines; the service list, the workarounds for a document's bugs and the base client stay in the repository that owns the documents
- the base client a generated package is built on is named per service (`ClientImport`, `Auth`), and a service can give its own `client.go` (`Client`) and a paragraph for the package documentation (`Notes`)
- which operations are lists is said per service (`Paging`): the two parameters and two properties, and whether the list pages by start index (Emby, Jellyfin) or by page number (Sonarr, Radarr, Prowlarr)
- taken from the Sonarr, Radarr and Prowlarr copies, each off unless a service asks: `PathPrefix` and `Words` for method names, `WrittenWhole` for settings saved whole, `SendRequired` for required properties, `PreferJSON` for a response that lists JSON beside another type, and `ExpectSameShape` for a failure that answers the success shape
- a generated package declares `DocumentVersion`, the version its document's file is named by, beside `APIVersion`, the document's own, which for some servers does not move from release to release
- `workarounds.Verify` is the test every workaround must pass, in one call: it applies to its vendored document, fails the second time, and does not panic on a document that has lost its 200 responses
- generated files start `// Code generated by pandorest; DO NOT EDIT.`; a file with the header of one of the earlier copies (`sdk/pandorest`, `internal/pandorest`) is still recognised as generated, so stale ones are removed on the first run
- a service with no lists gets no paged test server in its generated tests and no word of paging in its package documentation
