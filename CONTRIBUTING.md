# Contributing

Thanks for helping. This page says how to check a change, and how releases go out.

## Checking a change

```bash
make            # format and build
make test       # the unit tests
make testacc    # both live suites, each against a throwaway Sonarr in Docker
make check-all  # all of the above, and every linter
```

- **A new tool needs a live test.** The tool suite fails if a tool is never called.
- **A new kind of audit finding needs one too,** or a written reason why the test library cannot produce it.
- **Add a line to `CHANGELOG.md`,** under "Unreleased", in plain words.

How the tests work is in [docs/development.md](docs/development.md).

## Releases

- **Changes collect under "Unreleased"** in `CHANGELOG.md` as they land.
- **A version is tagged when a batch is worth upgrading for,** not for each change.
- **Breaking changes are grouped.** They ship together in one release, rather than one in each.
