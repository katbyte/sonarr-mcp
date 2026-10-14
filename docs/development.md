# Development

How sonarr-mcp is built and tested.

```bash
make            # format and build
make test       # the unit tests
make testacc    # both live suites, each against a throwaway Sonarr in Docker
make check-all  # all of the above, and every linter
make cover      # every suite, merged into one coverage figure
```

## Unit tests

`make test` needs nothing but Go, and is quick. It covers:

- how tools load, and the toolsets
- the rules behind each audit
- the settings: flags, config files and the HTTP token
- the Go client, against a stand-in server
- the fake indexer, the fake download client and the recorder the live suites use

It also builds the Go client afresh and fails if the copy checked in is out of date. And it applies each fix for Sonarr's API document twice, to prove each one notices when its mistake is gone.

## Live suites

Everything else runs against a real Sonarr in Docker, because a stand-in can only confirm what you already believed. The tests are under `acceptance/`, and each suite gets a container of its own.

| Suite | What it proves | Command |
|---|---|---|
| `acceptance/sonarr/` | The Go client. Every operation is called, bar four that restart, stop or restore Sonarr. | `make testacc-sdk` |
| `acceptance/tools/` | The tools: every tool, every audit, whole journeys, and the built program itself. | `make testacc-tools` |

They need Docker (or Colima), ffmpeg, jq and openssl. With no server to test against, they skip rather than fail.

### The client suite

- Each request must be one Sonarr accepts, and each answer must be read with its fields filled.
- The four operations left out are named in the suite, with the reason.
- A run fails if an operation was neither called nor named.

### The tool suite

- **Each tool on its own.** Finding a series by name, what each answer holds, searches, imports, renames.
- **Every audit.** Each runs against a library seeded with what it exists to find.
- **Real downloads.** A search grabs from the fake indexer, and the fake download client finishes the download.
- **Every kind of settings record.** Each is added, changed and deleted. A notification really sends, to a listener the suite runs.
- **Journeys.** A finding, the fix it names, then the audit again to see it gone. A stuck download blocklisted and replaced from a second indexer. A file below cutoff upgraded and replaced. A torrent found, handed over and imported.
- **A whole setup from nothing.** Tags, formats, profiles, an indexer, a download client, a series, a grab, an import and a backup are made with the tools alone. They are made the naive way, so the audits find what is wrong. Each fault is fixed with the tool its finding names. Then it is all taken down, and Sonarr is compared with how it started.
- **The built program.** It is run over stdio and over HTTP: its flags, the token check, and refusing to start without a key.

### Nothing ships untested

- The tool suite records every tool it calls. It fails if a tool was never called, so a new tool cannot ship untested.
- Each audit lists the kinds of problem it can report. A run fails if the test library never produced one and nothing says why.
- A few kinds cannot be produced on a real Sonarr: a download stuck for a day, a disk nearly full. Those are listed with their reason, and the unit test that covers them instead.

### No network needed

- A fake Newznab indexer, a fake Torznab one and a fake SABnzbd run inside the tests. They come from [go-kt](https://github.com/katbyte/go-kt).
- Sonarr's own calls out are recorded once and replayed. Those go to TheTVDB, Sonarr's own services and the artwork servers.
- The container is started to send its calls through the recorder, and to trust it.

```bash
make record         # record them again from the real services
make record-check   # check the recordings still match, without rewriting them
```

`record-check` compares the shape of each answer and ignores the values. It fails when a service renames or drops a field, not when an air date moves. The recordings keep only the suite's own series, out of every show a service knows.

### A library of thousands

No test Sonarr holds thousands of series, so that is a unit test. Three thousand series are paged, searched and audited against a stand-in that is slow to answer. It holds each audit to reading several series at once, and each only once.

## The test library

`.scripts/testenv.sh` builds it fresh for each run. Nothing is committed.

- **Tiny videos.** ffmpeg writes black frames and a silent sound track: about half a megabyte for a 45 minute episode. Sonarr reads a real resolution, length and language from each.
- **Every fault the audits look for.** On disk: a gap in a season, a file deleted behind Sonarr's back, a file copied in by hand, a folder Sonarr does not know. In the files: a 360p file named 1080p, five minutes of video for a 45 minute episode, a German dub. In the settings: an anime set up as a standard show, a continuing series ignoring new seasons, a tag nothing uses.
- **Set up through the tools.** The script only creates the container, with a known API key, and lays out the files. The suites add the root folder, the series, the indexer and the download client themselves, so setting Sonarr up is tested too.
- **Where it goes.** Under `~/.cache/sonarr-mcp`. `SONARR_TEST_DATA` moves it, but not to `$TMPDIR`, which Docker on a Mac does not share.

`.scripts/testenv.sh fixtures` writes the files alone, to look at.

## Coverage

`make cover` runs all three suites and merges them into one figure, the one the badge shows.

- `go test -cover` on its own reports too little, since most of the real work happens in the live suites.
- The merge uses Go's own tooling, with nothing added.
- The generated client is left out of the figure and reported on a line of its own. What proves it is the client suite calling its operations, not a count of lines.

## Dev tools

The tools are pinned in `.tools/go.mod` and at the top of the makefile, and make builds them into `.tools/bin`. If the checkout cannot run programs, point `TOOLS_BIN` somewhere that can: `make TOOLS_BIN=~/.cache/sonarr-mcp/bin lint`.

| Command | Checks |
|---|---|
| `make lint` | the Go code |
| `make typos` | spelling |
| `make markdownlint` | the markdown |
| `make linkcheck` | links in the docs |
| `make shellcheck` | the scripts |
| `make actionlint`, `make yamllint`, `make zizmor` | the workflows |
| `make depscheck` | that the vendored code matches `go.mod` |

How the Go client is generated is in [sdk.md](sdk.md).
