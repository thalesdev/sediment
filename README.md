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

The storage engine and the RESP server are done and measured. `redis-cli` and
`redis-benchmark` run against the server, go-redis works on RESP2 and RESP3, and a write
that had returned survives a `kill -9`.

| Part | State |
|---|---|
| Shale, the storage engine | Done: write-ahead log, memtable (a skiplist without a lock), SSTables, block cache, MANIFEST, compaction, group commit, atomic batches |
| `resp`, the protocol | Done: RESP2 and RESP3, fuzzed, with limits on every size |
| `sedimentd`, the RESP server | Done for strings, hashes and sets: the string and counter commands, key expiry (`SET EX`, `EXPIRE`, `TTL`, `PERSIST`), `HSET` and the hash commands, `SADD` and the set commands, `MULTI`, `EXEC` and `WATCH`, `SCAN` and `KEYS`, `RENAME`, `BGSAVE`, pipelining, `HELLO 3` |
| Security | TLS 1.3 and mTLS, users with key patterns, subcommand rules and selectors, `ACL SETUSER`, `ACL LOG`, users from a client certificate, a limit of failed sign ins by address, safe defaults |
| Public Go API | Not yet: Shale lives in `internal/shale` until the server shapes it |

The commands, types and ACL features that work, and the ones that do not, are in
[docs/RESP.md](docs/RESP.md). The threat model is in [SECURITY.md](SECURITY.md).

## Run it

With a container, in one command:

```bash
docker run -p 127.0.0.1:6379:6379 -v sediment-data:/data ghcr.io/thalesdev/sediment
```

Then `redis-cli` connects to `127.0.0.1:6379`. The image has no `redis-cli`. Its
health check is `sedimentd healthcheck`. The server inside the container listens on
every address of the container, and the container network keeps it private, so
publish the port on the loopback address of the host as above, and set up users and
TLS before you publish it to a network.

From the source:

```bash
go run ./cmd/sedimentd -dir ./data
```

Or download a build for Linux or macOS from the releases, or run `make dist`.

It listens on `127.0.0.1:6379`, with no TLS and no users. It refuses to listen on
an address that other machines reach unless it has TLS or a sign in. Settings go
in a file with `-config`, in the style of `redis.conf`.

To see inside a node: `INFO`, `CLIENT LIST`, `SLOWLOG GET`, `ACL LOG`, structured
logs (`log-format json`, and `SIGHUP` reads `loglevel` again), a Prometheus
endpoint (`metrics-addr`), OpenTelemetry metrics and traces over OTLP
(`otlp-metrics`, `otlp-traces`), and `sedimentd healthcheck` for a probe.

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

A command goes through the server before it reaches the engine:

```
 client
   |   TCP, or TLS 1.3 with an optional client certificate (mTLS)
   v
 connection        limit on clients, time to send a command, time to take a reply,
   |               and smaller limits on the size of a command before sign in
   v
 command check     unknown command, number of arguments, AUTH, then the ACL
   |               (categories such as @read, @write and @dangerous)
   v
 MULTI queue       between MULTI and EXEC a command waits here, with limits on
   |               the commands and the bytes. EXEC locks the keys of the queue,
   |               checks the WATCH, and runs the commands in order
   v
 command layer     size of the key, lock per key for a read-modify-write, the
   |               engine key and the typed value (string, hash, set), a deadline
   |               that EXPIRE sets, limit on the size of a reply
   v
 Shale             the engine, below
```

Inside Shale a write is a sequence, and a read looks at a few places:

```
                     Put / Delete                         Get
                          |                                |
                    write queue (group commit)       memtable
                          |                          frozen memtable
                1. room for the group?               tables, newest first
                   (freeze the memtable if it              |
                    is full, wait for a flush or           +--> bloom filter
                    the compaction if they lag)            +--> index block
                          |                                +--> one data block
                2. write-ahead log                                ^
                   (one record per group)                    block cache
                          |                                  (the file, on a miss)
                3. memtable (skip list, and the list of
                   range deletes of DeleteRange)
                          |
                4. last sequence number published, the write returns

 background:   frozen memtable --flush--> SSTable ---> MANIFEST (one edit per change)
               SSTables --size-tiered compaction--> fewer SSTables, with the old
               versions of a key and the expired keys dropped
```

The server keeps each key as a record with a class and a hash slot, and a hash or a
set as one record for the key and one for each field, named by an id that is never
used again. `DEL` of a hash of any size is one `DeleteRange` of its fields, which is
a record of the log and not one write for each field. The key layout is in
`server/command/keyenc.go`.

A snapshot and an iterator read a view that does not change while they run. `SCAN`,
`KEYS`, `DBSIZE` and `RANDOMKEY` open an iterator, read, and close it inside one command:

```
 DB.Snapshot / DB.NewIterator
   |
   1. take the read state (memtable, frozen memtable, tables),
   |  then the last sequence number
   |
   2. merge the sources in key order: the memtables, and the tables that can hold the range
   |
   3. for each key, return the newest version that is not above the sequence number,
   |  and hide a key whose newest version is a delete
   |
   4. Release or Close gives the state back. Until then the table files stay on disk,
      even if a compaction replaced them
```

A data block comes from the block cache when it is there, and from the file when it
is not. All the tables of a DB share one cache, 8 MiB by default and 64 MiB in
`sedimentd`. It helps a hot set of keys. A read at random over data much larger than
the cache is almost all misses.

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
| `resp` | The RESP2 and RESP3 reader and writer, without the network |
| `server` | The connection loop, the commands (`server/command`), the users (`server/acl`) and the settings (`server/config`) |
| `cmd/sedimentd` | The server program |
| `compat` | A separate module: compatibility tests and benchmarks against goleveldb, and the go-redis client against the server |

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

See [ROADMAP.md](ROADMAP.md): what is done, what was decided against, the path to v1.0
(a complete single node, safe operation, replication with Raft, release candidates),
and what comes after it.

## License

[MIT](LICENSE)
