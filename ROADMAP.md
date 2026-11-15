# Roadmap

sediment is a key-value database that speaks the Redis protocol (RESP), built on **Shale**, an LSM storage engine written from scratch in Go.

**Finish line (reached):** `redis-cli` connects and runs `GET`, `SET` and `DEL`, `redis-benchmark` runs against the server, and no acknowledged write is lost after a `kill -9`.

**Target:** v1.0, a replicated, secure, Redis compatible database for strings, hashes and sets. The path is in [Path to v1.0](#path-to-v10).

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
- **CRDTs and active-active multi-region.** Replication is Raft, in one region. Raft keeps one order of writes, which the atomic commands (`INCR`, `SETNX`, `GETDEL`) need.
- **A formal proof before the release candidates.** The TLA+ model is the last item of the project.
- **Using the comparison with Redis as the goal.** It is a ruler, and the numbers are kept, but the work is chosen by what the server needs. The CPU per command with one core is still 1.4 to 4.5 times that of Redis, and a user who needs the data set larger than memory or the durability is not helped by closing that gap.

## Path to v1.0

v1.0 is milestones 1 to 4. Each milestone ends with `make full` green and the docs updated. The order is the order of the work, except that milestones 1 and 2 can run together.

### Milestone 1: a complete single node
- [ ] **Key encoding.** One layout for the type, the hash slot and the user key, decided before the first new data type. The key layout is frozen at v1.0, so the slot prefix of cluster mode is part of it even though cluster mode comes later.
- [ ] **Hashes**: `HSET`, `HGET`, `HDEL`, `HEXISTS`, `HLEN`, `HGETALL`, `HKEYS`, `HVALS`, `HMGET`, `HSETNX`, `HINCRBY`, `HSCAN`. A hash is a range of keys, read with an iterator.
- [ ] **Sets**: `SADD`, `SREM`, `SISMEMBER`, `SMEMBERS`, `SCARD`, `SSCAN`, `SUNION`, `SINTER`, `SDIFF`.
- [ ] **Transactions**: `MULTI`, `EXEC`, `DISCARD`, `WATCH`.
- [ ] **The rest of the string and key commands**: `SETEX`, `PSETEX`, `MSETNX`, `GETEX`, `GETRANGE`, `SETRANGE`, `INCRBYFLOAT`, `UNLINK`, `RENAME`.
- [ ] **Backup**: a consistent copy of a running database, built on snapshots, and the way to restore it.

### Milestone 2: safe to run, and visible from the inside
- [ ] **ACL phase 2**: key patterns (`~prefix:*`, `%R~`, `%W~`), `ACL SETUSER`, `DELUSER`, `SAVE`, `GETUSER`, `LOG`, selectors, rules for subcommands, user from the CN of a client certificate.
- [ ] **Sign in protection**: a rate limit of attempts per address.
- [ ] **Operator commands**: `CLIENT LIST`, `CLIENT KILL`, `SLOWLOG`.
- [ ] **Logging**, as structured `slog`, with a `log-format` of text or JSON and a level that `SIGHUP` reloads.
  - The engine has no logger today. Give `shale.Options` a `Logger` and log the flush and the compaction (start, end, bytes, time), the stalls of the writes, what recovery replayed or cut, and a file that could not be removed.
  - The server logs the failed sign ins and the denied commands (user, address, command, never the key, the value or the password), the commands slower than a limit, and the connections that close with an error. The opening and closing of a connection is at debug level.
  - A line of a request carries the trace id, so a log and a trace meet.
- [ ] **Metrics** in OpenTelemetry format (OTLP), and a Prometheus endpoint, with what `INFO` shows today: commands by name and result, latency, clients, the waits of the writes, the flush, the compaction, the block cache, and the Go runtime. The Prometheus endpoint is the only HTTP port of the server. It is off by default, listens on `127.0.0.1`, and takes TLS and a sign in when it is opened to a network.
- [ ] **Tracing with OpenTelemetry.** A span for each command with the name, the number of keys (never a key), the result and the size of the reply, sampled by a rate in the settings. The flush and the compaction are spans of their own. Only the OpenTelemetry API is imported by the engine and the server, and `sedimentd` links the SDK, so a library user pays nothing. The context goes in the entries of the Raft log in milestone 3, so a write is one trace across the nodes.
- [ ] **Health, through RESP as Redis does it.** `PING` answers `PONG` when the node can serve, and an error when it cannot (while it loads or recovers, and in milestone 3 when it knows no leader). `INFO` shows the state: `loading`, and in milestone 3 `role`, `leader`, `raft_term` and `raft_applied_index`. `sedimentd healthcheck` speaks RESP, with TLS and a sign in if the node has them, and exits with 0 or 1, for the `HEALTHCHECK` of a container and the exec probe of Kubernetes, because a small image has no `redis-cli`. There is no HTTP port for health.
- [ ] **Measures on Linux**, with a container of one CPU (the scripts use the `ps` of macOS), before any number is said for a real deployment.
- [ ] **Packaging**: a container image, release builds for Linux and macOS, and a quickstart of one command in the README.

### Milestone 3: replication with Raft
- [ ] **One Raft group** of 3 or 5 nodes in one region. The leader takes the writes, the Raft log is separate from the WAL of Shale, Shale is the state machine, and a write returns after a majority has it.
- [ ] **Commands as entries of the log.** The read-modify-write commands (`INCR`, `SETNX`, `GETSET`, `GETDEL`, `APPEND`, `SET NX` and `XX`, `EXPIRE`) run in the apply step, in the order of the log, and not under a lock before the proposal. The leader stamps the deadline of a key, so every node sees the same one.
- [ ] **Snapshot transfer** to a new node or a late one, with the snapshots and the iterators of Shale.
- [ ] **Reads**: linearizable through the leader with a lease, and an explicit way to read from a follower that may be stale.
- [ ] **Membership**: add and remove a node, move the leader, TLS and mTLS between the nodes.
- [ ] **Failure tests**: a partition, `kill -9` of the leader, a full disk, a jump of the clock.

### Milestone 4: release candidates
- [ ] **Formats frozen**: WAL, SSTable, MANIFEST, the key layout and the Raft log, with the rule for an upgrade between versions.
- [ ] **Public Go API** of Shale, out of `internal`.
- [ ] **A review of the security** of the server layer and the nodes.
- [ ] **A long test**: days of load with kills and partitions, with no lost write and no wrong read.
- [ ] **RC1, RC2 and so on**, until one has no open bug. Then v1.0.

## After v1.0

In this order.
1. **Cluster mode**, for data that does not fit in one group: 16384 hash slots with hash tags, `CLUSTER SLOTS` and `CLUSTER SHARDS`, `MOVED` and `ASK`, one Raft group for each range of slots. A slot is one range of keys because of the key layout of milestone 1, so a slot moves with a range iterator and without a full scan. A command with keys in two slots is a `CROSSSLOT` error.
2. **More data types**: lists, sorted sets, `PUBLISH` and `SUBSCRIBE`, streams, each one on the key layout of milestone 1.
3. **A formal model in TLA+** of Shale (WAL, flush, MANIFEST, compaction, reference counting, `Get` ordering) and of the replication, checked across every interleaving and crash point. It is the last item of the project.

## Engine experiments

None of these is promised for v1.0. Each one starts with a measurement, and is dropped if the number does not pay for it.

- **Compaction**: leveled compared with size-tiered (write amplification is about 2.9), and incremental compaction, which frees the inputs piece by piece instead of needing up to 2x of disk.
- **Reads**: a `MultiGet` in Shale for pipelined `GET` and `MGET`, a cache of the index blocks and of the parsed blocks (a hit of the block cache still costs about 470 ns, most of it in the block iterator), and a better policy than "one miss in eight" for a cache that is full.
- **Writes**: less CPU in the write path (the flush and the compaction are about 20% of the CPU of a `SET`, and the log about as much), and key-value separation for large values.
- **Files and memory**: delete obsolete files outside the `Get` path, reuse the blocks of a memtable after its flush and measure the memory that a memtable really uses, and Snappy compression.
- **Iteration**: reverse iteration and `SeekToLast`, and snapshots that live across commands (they need a registry of the oldest live snapshot, so that a compaction keeps the versions that one can see).
- **Protocol**: RESP3 attributes and streamed strings, a second native protocol next to RESP, RESP over QUIC compared with TCP.
- **Tools**: a generator of load for the cases that `redis-benchmark` cannot do (a data set larger than the memory of Redis with `maxmemory`, keys of many sizes with common prefixes, large values, constant overwrites and deletes).
- **The CPU per command**: explain the 1.3 to 1.6 us between the benchmark without the network and the real run (the guess is a cold CPU cache, not tested).
