<p align="center">
  <img src="docs/img/sediment-icon.svg" alt="sediment" width="112" height="112">
</p>

<h1 align="center">sediment</h1>

<p align="center">
  A key-value database that speaks the Redis protocol, built on <b>Shale</b>,<br>
  an LSM storage engine written from scratch in Go.
</p>

<p align="center">
  <img alt="status: pre-alpha" src="https://img.shields.io/badge/status-pre--alpha-orange">
  <img alt="Go 1.27" src="https://img.shields.io/badge/go-1.27-00ADD8">
  <img alt="license: MIT" src="https://img.shields.io/badge/license-MIT-green">
</p>

---

Data settles in layers. Writes land in memory and in a log, get flushed to
sorted files, and are merged into older layers over time. That is a
log-structured merge-tree, and it is also how sediment forms. The trilobite in
the logo is what you find when you dig.

## Status

The storage engine is complete and measured. The server is not built yet.

| Part | State |
|---|---|
| Shale, the storage engine | Done: write-ahead log, memtable, SSTables, MANIFEST, compaction, group commit |
| `sedimentd`, the RESP server | Not started, see the [roadmap](ROADMAP.md) |
| Public Go API | Not yet: Shale lives in `internal/shale` until the server shapes it |

**Finish line:** `redis-cli` connects and runs `GET`, `SET` and `DEL`,
`redis-benchmark` runs against the server, and no acknowledged write is lost
after a `kill -9`.

## What Shale does

- **LevelDB formats on disk.** The log, the tables, the bloom filter and the
  internal key (user key plus a 56-bit sequence number and a kind) use the
  LevelDB layouts. [goleveldb](https://github.com/syndtr/goleveldb) reads the
  tables Shale writes, and Shale reads the tables goleveldb writes. The
  `compat` module tests this in both directions, against a real database.
- **Crash safe.** A write that has returned survives `kill -9`. A log that is cut
  or damaged at its end is treated as the end of the log. A MANIFEST edit is a
  single atomic commit, and tables that a damaged MANIFEST no longer names are
  moved to `lost/`, never deleted.
- **Background flush and compaction.** The memtable is written to a table by a
  goroutine while writes go on in a new memtable. A size-tiered compaction joins
  tables that are next to each other by age and have a similar size, and drops
  the versions that a newer one hides.
- **Group commit.** Concurrent writes share one log record and, with
  `Options.Sync`, one `fsync`.
- **Reads that do not block writes.** A read takes a consistent state of the
  memtable and the tables, and reference counts keep the files of a replaced
  table until the last read ends.
- **Tested hard.** Crash windows at every step of a flush and of a compaction,
  a `kill -9` test that kills real processes while many goroutines write,
  fuzzing of every decoder, and byte-for-byte checks against goleveldb.

## Performance

Shale against goleveldb on the same machine, with the workloads of the LevelDB
`db_bench`: keys of 16 bytes, values of 100 bytes, a memtable of 4 MiB, a bloom
filter, no compression, 1M operations. Lower is better.

| Workload | Shale | goleveldb |
|---|---:|---:|
| Fill random | 5.7 us/op | 5.6 us/op |
| Overwrite | 5.8 us/op | 6.6 us/op |
| Read random | 2.5 us/op | 3.1 us/op |
| Fill random, 8 goroutines | 3.4 us/op | 3.5 us/op |
| Fill sync, 8 goroutines | 635 us/op | 643 us/op |

Apple M3 Pro, macOS, Go 1.25 (measured before the move to 1.27). The method, the single-writer sync numbers, the
write amplification and the profiles are in [BENCHMARKS.md](BENCHMARKS.md).
Reproduce them with `make bench-compare`.

## How it works

```
                     Put / Delete                         Get
                          |                                |
                    write queue (group commit)       memtable
                          |                          frozen memtable
              +-----------+-----------+              tables, newest first
              |                       |                    |
        write-ahead log          memtable (skip list)      +--> bloom filter
        (one record per group)         |                   +--> index block
                                       | full              +--> one data block
                                       v
                              flush (background)
                                       |
                                       v
                       SSTables ---> MANIFEST (one edit per change)
                          |
                          v
                 size-tiered compaction (background)
```

Every internal key carries a sequence number. A table has a range of sequence
numbers, and the ranges do not overlap, so the order of the tables is the order
of their ages.

## Repository layout

| Path | What it holds |
|---|---|
| `internal/shale` | The engine: `DB`, recovery, flush, compaction, group commit |
| `internal/wal` | The LevelDB log format |
| `internal/sstable` | The LevelDB table format: writer, reader, block iterators |
| `internal/bloom` | The LevelDB bloom filter |
| `internal/skiplist` | The memtable |
| `internal/merge` | K-way merge of sorted iterators |
| `internal/manifest` | The record of a change of the set of table files |
| `internal/batch` | The write batch format |
| `internal/ikey` | The internal key |
| `compat` | A separate module: compatibility tests and benchmarks against goleveldb |

## Development

You need Go 1.27 or later.

```bash
make test        # fast tests, a few seconds
make race        # the same, with the race detector
make full        # everything: checks, all tests, race, compat and fuzz
make bench       # benchmarks of the packages
make bench-compare   # Shale against goleveldb, slow
make help        # all the targets
```

`make full` takes a few minutes. It runs the slow tests, including the ones that
kill processes, and every fuzz target for 10 seconds each (`FUZZTIME=30s` to
change it).

## Roadmap

See [ROADMAP.md](ROADMAP.md). Next is the RESP server, then the items of the
backlog, each with the reason it would be picked up.

## License

[MIT](LICENSE)
