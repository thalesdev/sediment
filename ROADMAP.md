# Roadmap

sediment is a key-value database that speaks the Redis protocol (RESP), built on **Shale**, an LSM storage engine written from scratch in Go.

**Finish line:** `redis-cli` connects and runs `GET`, `SET` and `DEL`, `redis-benchmark` runs against the server, and no acknowledged write is lost after a `kill -9`.

## Done

### Primitives
- [x] **Skiplist**: the memtable, first with a mutex (v1) and now in blocks without pointers and without a lock (v2).
- [x] **WAL**: the LevelDB log format (32 KiB blocks, fragmented records, masked CRC-32C), validated against RFC 3720 vectors and recovery from truncation at every byte.
- [x] **Bloom filter**: byte-compatible with LevelDB, validated against goleveldb and against the theoretical false positive rate.
- [x] **SSTable**: the LevelDB table format (prefix compression, restart points, index, filter block, footer). goleveldb reads our tables and we read theirs.
- [x] **K-way merge**: merges any number of sorted iterators, without deduplication.

### Shale engine
- [x] **6.0 Internal key**: user key plus a 56-bit sequence number and a kind, the same layout as LevelDB, proven against a real goleveldb database.
- [x] **6.1 Memtable and WAL**: `Open`, `Put`, `Get`, `Delete`, `Close`, write batches, directory lock, recovery after `kill -9`.
- [x] **6.2 Flush**: background flush to SSTables, MANIFEST with a single commit point, crash windows covered, `lost/` for tables a damaged MANIFEST no longer references.
- [x] **6.3 Compaction**: size-tiered, tables ordered by sequence number, reference counting, write stall.
- [x] **6.4 Performance**: measured against goleveldb on the same machine, and within 1.02x of it on every workload (`make bench-compare`, `BENCHMARKS.md`).
  - Twin benchmarks and profiles.
  - Index block validated once at open, not on every lookup (`ReadRandom` from 24.5 to 2.5 us).
  - One `write` per WAL record, so a `kill -9` no longer tears a fragment.
  - Fewer allocations on the write path (7 to 3 per write).
  - Group commit for concurrent writers (`FillSyncParallel` from 2250 to 602 us).

### 7. Server (`sedimentd`)
- [x] **`resp`**: RESP2 and RESP3 reader and writer without the network, a limit on every size, allocation that follows the bytes that arrive, fuzzed. See [docs/RESP.md](docs/RESP.md).
- [x] **Command layer** separate from the protocol, so another protocol can be added later.
- [x] **Commands**: `GET`, `SET` (`NX`, `XX`, `GET`, `EX`, `PX`, `EXAT`, `PXAT`, `KEEPTTL`), `DEL`, `EXISTS`, `MGET`, `MSET`, `INCR`, `DECR`, `INCRBY`, `DECRBY`, `APPEND`, `STRLEN`, `SETNX`, `GETSET`, `GETDEL`, `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT`, `TTL`, `PTTL`, `EXPIRETIME`, `PEXPIRETIME`, `PERSIST`, `SCAN`, `KEYS`, `DBSIZE`, `RANDOMKEY`, `TYPE`, `PING`, `ECHO`, `QUIT`, `SELECT`, `AUTH`, `HELLO`, `CLIENT`, `COMMAND`, `INFO`, `CONFIG GET`, `ACL` (`WHOAMI`, `USERS`, `LIST`, `LOAD`, `CAT`).
- [x] **Key expiry** (TTL): every value has a 1 byte tag, and 8 more bytes of deadline in Unix milliseconds if the key expires. Reads treat an expired key as missing, and a compaction drops it through `Options.CompactionFilter`. The engine format did not change. A data directory written before this change holds values without the tag and is not readable.
- [x] **Snapshots and range iterators** in Shale (`DB.Snapshot`, `DB.NewIterator`, bounds, `Seek`): a view is a read state plus a sequence number, so a flush or a compaction does not change it. It holds the memtables and the table files that it reads until it is released, so it is for short reads. The server runs `SCAN`, `KEYS`, `DBSIZE`, `RANDOMKEY` and `TYPE` with it, one iterator for each command.
- [x] **Atomic multi-key writes** in Shale (`DB.Write`), for `MSET` and `DEL` of many keys.
- [x] **Pipelining**, with a flush only when the reader would block.
- [x] **Per-connection panic recovery**, one goroutine per connection.
- [x] **Security**: TLS 1.3 and mTLS, users from an ACL file (PBKDF2 passwords, command and category rules, fail closed), tighter limits before sign in, slow down after failed sign ins, safe bind by default, HTTP cross-protocol guard. See [SECURITY.md](SECURITY.md).
- [x] **`sedimentd`**: settings from a `redis.conf` style file and a few flags, graceful stop on `SIGINT` and `SIGTERM`, `SIGHUP` reloads the TLS certificate, `sedimentd hash-password`.
- [x] **Compatibility with go-redis** on RESP2 and RESP3, with sign in and pipelines (`compat`).
- [x] **Benchmarks** with and without pipelining (`make bench-server`, and `redis-benchmark`).
- [x] **Finish line**: `redis-cli` runs `GET`, `SET` and `DEL`, `redis-benchmark` runs against the server, and a write that had returned was there after a `kill -9`.

### 8. Performance and observability of the server
Each change was made after a measurement, with the numbers in [BENCHMARKS.md](BENCHMARKS.md).
- [x] **The `SET` commands of a pipeline run as one write** (`SetMany`), with shared locks per stripe for plain writes. A pipelined `SET` with `-P 64` went from 428,000 to 915,000 per second.
- [x] **Skiplist in blocks without pointers and without a lock**: `SET` through the connection loop 31% faster on one core, parallel reads of the memtable 59% faster, 0 allocations per insert. The memtable size is limited to 1 GiB, and a list that has no room returns `ErrFull`, which fails the DB.
- [x] **Block cache** for `GET`: a hot set is read 51% faster with one connection and 68% faster with 50, with the default 8 MiB (64 MiB in `sedimentd`).
- [x] **p99 with one core**: with one processor the scheduler of Go looks for network events only when its queues are empty, and the flush and the compaction never let them empty. A pause of 20 us every 128 entries (only with one processor) took the p99 of a pipelined `SET` from 27 to 10 ms.
- [x] **`INFO`** shows the waits of the writes for the flush and the compaction, the time of the flushes, the compactions and the freezes, the cache of blocks, the size of the batches of `SET`, and, from the runtime of Go, the scheduling latency and the pauses of the garbage collector.

## Decided against

Measured or discussed, and not to be proposed again without a new reason.
- **An event loop for the network layer.** A `PING` with one core costs the same as in Redis (1.0 us per command), so the layer of Go is not the cost.
- **A WAL on a file that is already allocated.** It would save 0.1 us per `SET`.
- **Fewer allocations in the parser.** The whole server layer is 0.17 to 0.31 us per command.
- **Time slices and an adaptive rule for the pause of the flush.** 15% better p95 with a pipeline of 16, 15% worse with 64, so the simple setting stayed.
- **Plain SHA-256 password hashes** (the `#hash` of the Redis ACL file): cheap to guess from, so the file is refused.
- **Key patterns that are read and not enforced**: the ACL file is refused until they are enforced (ACL phase 2).
- **`unsafe`**, anywhere.
- **Using the comparison with Redis as the goal.** It is a ruler, and the numbers are kept, but the work is chosen by what the server needs. The CPU per command with one core is still 1.4 to 4.5 times that of Redis, and a user who needs the data set larger than memory or the durability is not helped by closing that gap.

## Next

Nothing is started. The candidates, by what each one unlocks:
- **A formal model of Shale in TLA+.** It was planned as the first item after the finish line, and the last changes (memtable, flush, block cache, order of reads) touched the parts that it covers.
- **ACL phase 2**, which is the debt of the first phase of the security work.

## Backlog

Each item has the reason it would be picked up.

| Item | When |
|---|---|
| Formal model of Shale in TLA+ (WAL, flush, MANIFEST, compaction, reference counting, `Get` ordering), checked across every interleaving and crash point | the first of the candidates in "Next" |
| Snapshots that live across commands (for `MULTI`, `EXEC`, `WATCH`): they need a registry of the oldest live snapshot, so that a compaction keeps the versions that one can see | when a command needs a view that lasts more than one command |
| Reverse iteration and `SeekToLast` | when a command needs the last key |
| ACL phase 2: key patterns (`~prefix:*`, `%R~`, `%W~`), `ACL SETUSER`, `DELUSER`, `SAVE`, `LOG`, selectors, rules for subcommands, user from the CN of a client certificate | when a deployment needs more than one kind of client on the same data |
| Rate limit of sign in attempts per address, and a log of denied commands | before the server is exposed to untrusted networks |
| `CLIENT LIST`, `CLIENT KILL`, slow log | when an operator needs to see and cut clients |
| RESP3 attributes and streamed strings | only if a client needs them |
| A second, native protocol next to RESP | only with a concrete need RESP cannot express |
| RESP over QUIC, compared with TCP | experiment |
| Leveled compaction, compared with size-tiered | experiment, if write amplification (about 2.9) or space matters |
| Incremental compaction (free inputs piece by piece instead of needing up to 2x disk) | if large merges become a disk space problem |
| Delete obsolete files outside the `Get` path (a queue drained by the compactor) | if a measure ties a latency spike to the removal of a file |
| Reuse the blocks of a memtable after its flush, and measure the memory that a memtable really uses | if the memory of the server matters |
| A cache of the index blocks and of the parsed blocks (a hit of the block cache still costs about 470 ns, most of it in the block iterator) | if a workload of hot reads needs it, or a better policy than "one miss in eight" for a cache that is full |
| Less CPU in the write path of the engine (the flush and the compaction are about 20% of the CPU of a `SET`, and the log about as much), and key-value separation for large values | when the throughput of writes or the write amplification matters |
| Explain the 1.3 to 1.6 us per command between the benchmark without the network and the real run (the guess is a cold CPU cache, not tested) | when the CPU per command matters again |
| A `MultiGet` in Shale (one read state and one sequence number for many keys), for pipelined `GET` and `MGET` | if a profile of pipelined `GET` shows that cost |
| OpenTelemetry at request level in the server, sampled | if the server needs distributed tracing |
| Snappy compression | if disk usage matters more than CPU |
| A generator of load for the cases that `redis-benchmark` cannot do (a data set larger than the memory of Redis with `maxmemory`, keys of many sizes with common prefixes, large values and their write amplification, constant overwrites and deletes) | when we compare with Redis again, or when a user has one of those cases |
| The same measures on Linux (the scripts use the `ps` of macOS), with a container of one CPU, and `perf` or the CPU profile of Go, which is reliable there | before we say a number for a real deployment, and to decide if `io_uring` is worth it |
