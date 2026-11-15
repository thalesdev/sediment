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
- [x] **Commands**: `GET`, `SET` (`NX`, `XX`, `GET`), `DEL`, `EXISTS`, `MGET`, `MSET`, `PING`, `ECHO`, `QUIT`, `SELECT`, `HELLO`, `CLIENT`, `COMMAND`, `INFO`, `CONFIG GET`.
- [x] **Atomic multi-key writes** in Shale (`DB.Write`), for `MSET` and `DEL` of many keys.
- [x] **Pipelining**, with a flush only when the reader would block.
- [x] **Per-connection panic recovery**, one goroutine per connection.
- [x] **Security**: TLS 1.3 and mTLS, users from an ACL file (PBKDF2 passwords, command and category rules, fail closed), tighter limits before sign in, slow down after failed sign ins, safe bind by default, HTTP cross-protocol guard. See [SECURITY.md](SECURITY.md).
- [x] **Benchmarks** with and without pipelining (`make bench-server`, and `redis-benchmark`).
- [x] **Finish line**: `redis-cli` runs `GET`, `SET` and `DEL`, `redis-benchmark` runs against the server, and a write that had returned was there after a `kill -9`.

## Next

### 8. Server performance
- [ ] **CPU per command**: after the skiplist without a lock and the block cache, with one core each, the CPU per command of sediment is 1.4 to 4.5 times that of Redis (3.83 against 1.83 us for `SET` with `-P 16`, 3.00 against 0.67 us for `GET` with `-P 64`), and it fell by 15% to 25% in the two changes. See [BENCHMARKS.md](BENCHMARKS.md). The network layer is at parity with Redis (`PING`), and the server layer costs 0.17 to 0.31 us, so what is left is in the engine: the flush and the compaction (about 20% of the CPU of a `SET`), the write to the log, and the read of cold blocks. About 1.3 to 1.6 us per command between the benchmark without the network and the real run is still not explained.
- [x] **p99 with one core**: the cause was the scheduler of Go, which with one processor looks for network events only when its queues are empty, and the flush and the compaction never let them empty. A pause of 20 us every 128 entries (only with one processor) took the p99 of a pipelined `SET` from 27 to 10 ms and the maximum from 80 to 25 ms. See [BENCHMARKS.md](BENCHMARKS.md). The p95 rose from 5 to 8 ms; the variants that lower it (time slices of 40 us with a rule that does not pause when the writes are about to stall) are 15% better with a pipeline of 16 and 15% worse with 64, so they were not adopted (see BENCHMARKS.md). Left to do: measure it on Linux in a container with one CPU.
- [ ] **Where the LSM can win**, to measure with a generator of load that `redis-benchmark` cannot be: a data set larger than the memory of Redis (`maxmemory`), keys of many sizes and with common prefixes, large values (and the write amplification of a large value, which key-value separation would fix), and constant overwrites and deletes (fragmentation and the rewrite of the AOF in Redis against the compaction here).
- [x] **Block cache** for `GET`: a hot set is read 51% faster with one connection and 68% faster with 50, with the default 8 MiB (see [BENCHMARKS.md](BENCHMARKS.md)). Left to do: a filter or a policy better than "one miss in eight" if a workload needs it, and a cache of the index blocks and of the parsed blocks (a hit still costs about 470 ns, most of it in the block iterator).
- [x] **Skiplist in blocks without pointers and without a lock**: `SET` through the connection loop 31% faster on one core and 17% on 11, parallel reads of the memtable 59% faster, 0 allocations per insert. See [BENCHMARKS.md](BENCHMARKS.md). The memtable size is limited to 1 GiB, and a list that has no room returns `ErrFull`, which fails the DB. Left to do: reuse the blocks of a memtable after its flush, and measure the memory that a memtable really uses.
- [x] Not worth it, measured: the WAL on a file that is already allocated (0.1 us per `SET`), and the allocations of the parser (the whole server layer is 0.17 to 0.31 us).
- [ ] Block pool or block cache for `GET` (about 4.9 KiB per command), after the profile confirms it.
- [x] Run the consecutive `SET` commands of a pipeline as one write (`SetMany`), after the numbers showed one wait for the log per command.
- [ ] A `MultiGet` in Shale (one read state and one sequence number for many keys), for pipelined `GET` and `MGET`, only if a profile of pipelined `GET` shows that cost.

## Backlog

Each item has the reason it would be picked up.

| Item | When |
|---|---|
| Formal model of Shale in TLA+ (WAL, flush, MANIFEST, compaction, reference counting, `Get` ordering), checked across every interleaving and crash point | first item after the finish line |
| Delete obsolete files outside the `Get` path (a queue drained by the compactor) | if `redis-benchmark` shows latency spikes |
| Publish the goleveldb comparison in the README | now, 6.4 is done |
| Leveled compaction, compared with size-tiered | experiment, after the finish line |
| Incremental compaction (free inputs piece by piece instead of needing up to 2x disk) | if large merges become a disk space problem |
| Snapshots, iterators and `SCAN` | when a command needs a range read |
| RESP over QUIC, compared with TCP | experiment, after the finish line |
| A second, native protocol next to RESP | only with a concrete need RESP cannot express |
| Event loop server on `httpx` (epoll, kqueue, IOCP), compared with the Go netpoller | experiment, after the finish line |
| OpenTelemetry at request level in the server, sampled | if the server needs distributed tracing |
| Snappy compression | if disk usage matters more than CPU |
| Block cache | if a larger working set makes `ReadRandom` slow (it is 0.8x of goleveldb with 1M keys) |
| ACL phase 2: key patterns (`~prefix:*`, `%R~`, `%W~`), `ACL SETUSER`, `DELUSER`, `SAVE`, `LOG`, selectors, rules for subcommands, user from the CN of a client certificate | when a deployment needs more than one kind of client on the same data |
| Rate limit of sign in attempts per address, and a log of denied commands | before the server is exposed to untrusted networks |
| TTL in the engine and `SET ... EX`, `EXPIRE`, `TTL` | when a client needs keys that expire |
| Atomic read-modify-write on a key: `INCR`, `DECR`, `APPEND`, `SETNX`, `GETSET` | when a client needs counters (the lock per key already exists) |
| `CLIENT LIST`, `CLIENT KILL`, slow log | when an operator needs to see and cut clients |
| RESP3 attributes and streamed strings | only if a client needs them |
