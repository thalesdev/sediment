# Instructions for AI coding agents

This file is for tools that write or review code in this repository. People can
read it too. [CONTRIBUTING.md](CONTRIBUTING.md) has the rules for everyone.

## What this project is

sediment is a key-value database that speaks RESP (the Redis protocol). It is
built on Shale, an LSM storage engine in `internal/shale`. The RESP server is in
`server`, with the protocol in `resp` and the commands in `server/command`. See [README.md](README.md) and [ROADMAP.md](ROADMAP.md).

The module is `github.com/thalesdev/sediment`. The folders `compat` and
`cmd/sedimentd` are separate modules. `compat` has the compatibility tests and
benchmarks against goleveldb. `cmd/sedimentd` links the OpenTelemetry SDK, the
exporters and the Prometheus client, so that the main module imports only the
OpenTelemetry API.

## Commands

```bash
make check       # gofmt and vet, the three modules
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
- The engine and the server import only the OpenTelemetry API (`otel`,
  `otel/metric`, `otel/trace`). The SDK, the exporters and anything else that
  costs a library user stay in `cmd/sedimentd`. A `Meter`, a `Tracer` and a
  `Logger` that is nil mean no work at all.
- A log line, a metric attribute, a span attribute and a slow log entry never
  hold a key, a value, an argument or a password. A command is timed in one place,
  `Server.observe`, which every command passes, and a change there is measured
  with `make bench-inproc` and `benchstat`.
- An ACL check goes through `Env.Check`, with the keys of the command from
  `command.KeysOf`. A path that runs a command without `execute` (the batch of
  `SET`, `EXEC`) checks the keys too.
- Every engine key of the server goes through `server/command/keyenc.go`, and every value through its record codec. A command never builds a key by hand.
- A command takes the lock of a key through the client (`cl.lock`), and never through `env.Locks`. `EXEC` holds the locks, and they are not reentrant.
- A range delete has a sequence number, and it hides a version of a key only if that number is above the one of the version. A source that has a range delete and no version of the key hides the key in every older source, because the sequence ranges of the tables do not overlap.
- A checkpoint takes only the tables of a state, after a flush, and a directory appears under its name only with a rename, whole.
- A table is closed and removed only when the last read that uses it ends. A snapshot and an iterator are reads that last: each holds a read state and a sequence number, takes the state before the sequence number as `Get` does, and gives the state back in `Release` or `Close`. Close them before `DB.Close`.

## Layout

| Path | What it holds |
|---|---|
| `internal/shale` | The engine: `DB`, snapshots, iterators, recovery, flush, compaction, group commit |
| `resp`, `server`, `server/command`, `server/acl` | The protocol, the connections, the commands and the users |
| `internal/wal`, `sstable`, `bloom`, `skiplist`, `merge`, `manifest`, `batch`, `ikey` | The parts, one package each |
| `compat` | goleveldb tests and the twin benchmarks (separate module) |
| `cmd/sedimentd` | The server program, with the OpenTelemetry SDK and the exporters (separate module) |
| `internal/glob` | The glob matcher of key patterns and `KEYS` |

The server keeps its keys in the layout of `server/command/keyenc.go`, and a change of that layout is a change of a file format.

Some tests in `internal/shale` use unexported hooks in `Options` (`flushHook`,
`compactHook`, `getHook`, `iterHook`, `disableCompaction`) and `skipSync`. They are only for
tests. Do not use them in the engine.

## Tests that take time

`make test` skips the slow tests (`-short`) and skips the flush of files. The CI
and `make full` run everything, including tests that start a process and kill it
with `SIGKILL`. Do not remove a slow test to make a run faster. Make it faster,
or run it only outside `-short`.
