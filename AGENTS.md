# Instructions for AI coding agents

This file is for tools that write or review code in this repository. People can
read it too. [CONTRIBUTING.md](CONTRIBUTING.md) has the rules for everyone.

## What this project is

sediment is a key-value database that will speak RESP (the Redis protocol). It is
built on Shale, an LSM storage engine in `internal/shale`. The RESP server does
not exist yet. See [README.md](README.md) and [ROADMAP.md](ROADMAP.md).

The module is `github.com/thalesdev/sediment`. The folder `compat` is a separate
module with the compatibility tests and benchmarks against goleveldb.

## Commands

```bash
make check       # gofmt and vet, both modules
make test        # fast tests (-short), seconds
make race        # fast tests with the race detector
make full        # everything: check, all tests, race, compat, fuzz (minutes)
make bench-compare   # Shale against goleveldb, slow
```

Run `make check` and `make test` after every change. Run `make full` before you
say that a change to the engine, a format or the recovery is done.

## Rules that are not optional

- **Do not commit, push or open a pull request** unless the user asks.
- **Disclose the use of AI.** A commit that you wrote or changed in a significant
  way has a `Co-Authored-By` line for the tool, and the pull request description
  says that a tool was used. Never hide it.
- **Comments are godoc only.** Every package, type, function and method in a file
  that is not a test has a godoc comment. There are no other comments, no inline
  comments and no trailing comments. Write them in plain, short English, in the
  active voice. Test files have none.
- **Every `.go` file starts with `// SPDX-License-Identifier: MIT`** and a blank
  line.
- **Tests use `t.Run("Should ...", ...)`.**
- **Bytes from disk are not trusted.** They become errors, never panics. A panic is
  only for a bug of the caller. Fuzz every decoder.
- **No `unsafe`.** No em dash and no en dash in any text, code or documentation.
- **File formats do not change** (WAL, SSTable, bloom, MANIFEST, write batch,
  internal key) without a test in `compat` that checks the format against
  goleveldb.
- **Measure before you optimize.** Use `benchstat` with at least 10 runs. A change
  that does not improve the number is removed.

## Invariants of the engine

Break one of these and data is lost or returned wrong. Read the tests before you
change the code near them.

- Internal keys are ordered by `ikey.Compare`, never by `bytes.Compare`.
- `Get` takes the read state first and the last sequence number after it. The
  other order can miss a version that a compaction dropped.
- Tables are ordered by their range of sequence numbers, not by file number, and
  the ranges do not overlap.
- A change of the set of files is one MANIFEST edit. `Open` has one commit point,
  the rename of `MANIFEST.tmp`. The `wal.Writer` that wrote the MANIFEST is kept
  and reused, because the blocks of the log must stay in line.
- If the MANIFEST had bytes that `Open` skipped, `Open` never removes a table file.
  It moves the files to `lost/`.
- A write returns only after its record is in the log (and flushed, with
  `Options.Sync`). The memtable is updated after the log, and the sequence number
  is published last.
- A table is closed and removed only when the last read that uses it ends.

## Layout

| Path | What it holds |
|---|---|
| `internal/shale` | The engine: `DB`, recovery, flush, compaction, group commit |
| `internal/wal`, `sstable`, `bloom`, `skiplist`, `merge`, `manifest`, `batch`, `ikey` | The parts, one package each |
| `compat` | goleveldb tests and the twin benchmarks |

Some tests in `internal/shale` use unexported hooks in `Options` (`flushHook`,
`compactHook`, `getHook`, `disableCompaction`) and `skipSync`. They are only for
tests. Do not use them in the engine.

## Tests that take time

`make test` skips the slow tests (`-short`) and skips the flush of files. The CI
and `make full` run everything, including tests that start a process and kill it
with `SIGKILL`. Do not remove a slow test to make a run faster. Make it faster,
or run it only outside `-short`.
