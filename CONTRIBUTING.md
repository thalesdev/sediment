# Contributing

Thanks for your interest in sediment. The project is pre-alpha, so the best way
to help is to read the code, run the tests, and open an issue before a large
change. Check [ROADMAP.md](ROADMAP.md) to see what is planned and why.

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). To report
a vulnerability, or any way to lose data, read [SECURITY.md](SECURITY.md) and do
not open a public issue.

## Setup

You need Go 1.25 or later. There are no other dependencies. The `compat` folder is
a separate module that depends on goleveldb.

```bash
git clone https://github.com/thalesdev/sediment
cd sediment
make test        # fast tests, a few seconds
```

## Before you open a pull request

| Command | When |
|---|---|
| `make check` | always: format and vet |
| `make test` | always: the fast tests |
| `make race` | always: the fast tests with the race detector |
| `make full` | when you touch the engine, a file format or the recovery: all tests, race, compat and fuzz |

CI runs the full set on Linux and macOS. A pull request needs a green CI.

## What a good change looks like

- **One change per pull request**, with a clear reason. Open an issue first for
  anything large.
- **Tests.** New behavior has tests. A bug fix has a test that fails without the
  fix. Tests use `t.Run("Should ...", ...)`.
- **Data from disk is not trusted.** Bytes that come from a file become errors and
  never panics. A panic is only for a bug of the caller.
- **File formats do not change** without a compatibility test in `compat` that
  reads and writes the format with goleveldb.
- **Performance changes need numbers.** Measure before and after with
  `benchstat`, for example `make bench-compare BENCHCOUNT=10`, and put the result
  in the pull request. A change that does not improve the number does not go in.

## Code style

- Every file starts with `// SPDX-License-Identifier: MIT` and a blank line.
- Comments are godoc comments only: on packages, types, functions and methods in
  files that are not tests, and no other comments. Write them in plain, short
  English, in the active voice. Every function and type has one.
- No `unsafe`.
- `gofmt` is enforced by `make check`.

## Commits and pull requests

- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/)
  in English: `feat(shale): add group commit`, `fix(wal): ...`, `test(...)`,
  `docs`, `refactor`, `perf`.
- Fill in the pull request template.

## AI assisted changes

Tools that write code are welcome, under the same rules as any other change: you
are responsible for what you submit, and you must understand it, test it and be
able to explain it.

**You must say so when you used one.** If an AI tool wrote or changed a
significant part of a commit or a pull request, say it in the pull request
description, and add a `Co-Authored-By` line to the commit. You do not need to
disclose spell checking or simple autocomplete. A pull request that hides the use
of a tool, and that the author cannot explain, can be closed.

[AGENTS.md](AGENTS.md) has the instructions for the tools themselves.

## License

By contributing you agree that your work is licensed under the [MIT License](LICENSE).
