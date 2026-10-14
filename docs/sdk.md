# The Go client

`sdk/sonarr` is a complete Go client for Sonarr's v3 API. It knows nothing of MCP. If you only want to talk to Sonarr from Go, take the package and ignore the rest.

## Using it

```go
import "github.com/katbyte/sonarr-mcp/sdk/sonarr"

c, err := sonarr.New("http://nas:8989", os.Getenv("SONARR_TOKEN"))

series, err := c.GetSeries(ctx) // options may be left out when none is needed
for _, s := range series.Answer { ... }

specials, err := c.GetEpisode(ctx, sonarr.GetEpisodeOptions{SeriesId: series.Answer[0].Id, SeasonNumber: new(0)}) // a number or a flag is a pointer, so 0 and false can be asked for

missing, err := c.GetWantedMissingComplete(ctx, sonarr.GetWantedMissingOptions{Monitored: new(true), IncludeSeries: new(true)}) // every page
for _, e := range missing.Items { ... }

for h, err := range c.GetHistoryEach(ctx) { // a page at a time, until the loop stops
    if err != nil || h.SourceTitle == wanted { break }
}

rescan, err := c.RescanSeriesAndWait(ctx, sonarr.RescanSeriesCommand{SeriesId: series.Answer[0].Id}, time.Minute) // a command, queued and waited for
```

## What it gives you

- **A method for every one of Sonarr's 239 operations.** Each has typed options, a typed body and a typed answer.
- **Every page of a list.** A paged list has a `Complete` form that reads every page, and an `Each` form that reads a page at a time.
- **Commands that wait.** Each command Sonarr can be asked to run has a method, and a form that waits for it to finish.
- **An id that cannot be mixed up.** Each kind of id has a type of its own: `SeriesID`, `EpisodeID`, `EpisodeFileID`, `TagID`, `QualityProfileID`, `CommandID`, `TvdbID`. An episode's id handed where a series' belongs does not compile. `sonarr.SeriesID(n)` makes one from a plain number.
- **Errors for anything unexpected.** Each operation knows the answers Sonarr gives it. Anything else is an error.
- **No redirects.** It refuses a redirect rather than following one, so give it the address Sonarr itself answers on.

`sonarr.DocumentVersion` says which Sonarr release the API was taken from.

## How it is made

The client is generated, not written by hand, by [pandorest](https://github.com/katbyte/pandorest). That is the generator the katbyte MCP servers share, modelled on [hashicorp/pandora](https://github.com/hashicorp/pandora).

1. **Import.** Sonarr's own API document, in `api-defs/`, is turned into checked-in definitions, one file per group of operations. [api-defs/README.md](../api-defs/README.md) has the detail.
2. **Fix.** Sonarr's document has mistakes. Each is put right by a named fix in `sdk/pandorest`. A fix fails once Sonarr corrects the mistake, so none outlives its need.
3. **Compare.** `make pandorest-diff` reports what a newer document would change.
4. **Generate.** One file is written for each operation and each model.

`sdk/sonarr/service.go` is the one file written by hand. It holds how Sonarr's refusals read, and what is known of them.

## How it is checked

- `make apicheck` checks that every operation in Sonarr's document has a method.
- `make gencheck`, and the unit tests, fail when the generated code is out of date.
- The client suite, `acceptance/sonarr`, calls the operations against a running Sonarr. That is the only thing that catches Sonarr answering differently from what its document says.

More on the tests is in [development.md](development.md).
