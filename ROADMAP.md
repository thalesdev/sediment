# Roadmap

sediment is a key-value database that speaks the Redis protocol (RESP), built on **Shale**, an LSM storage engine written from scratch in Go.

**Finish line:** `redis-cli` connects and runs `GET`, `SET` and `DEL`, `redis-benchmark` runs against the server, and no acknowledged write is lost after a `kill -9`.

## Done

### Primitives
- [x] **Skiplist**: the memtable, with a mutex (v1).
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

## Next

### 7. Server (`sedimentd`)
- [ ] RESP2 over TCP, with `net` from the standard library and one goroutine per connection.
- [ ] A command layer separate from the protocol parser, so another protocol can be added later.
- [ ] `GET`, `SET`, `DEL`, `PING`, plus whatever `redis-cli` and `redis-benchmark` need to start.
- [ ] Pipelining.
- [ ] `INFO` backed by the engine metrics.
- [ ] Per-connection panic recovery.
- [ ] Benchmarks with and without pipelining.

## Backlog

Each item has the reason it would be picked up.

| Item | When |
|---|---|
| Formal model of Shale in TLA+ (WAL, flush, MANIFEST, compaction, reference counting, `Get` ordering), checked across every interleaving and crash point | first item after the finish line |
| Delete obsolete files outside the `Get` path (a queue drained by the compactor) | if `redis-benchmark` shows latency spikes |
| Publish the goleveldb comparison in the README | now, 6.4 is done |
| Lock-free skiplist (v2) | if the profile shows the skiplist as the bottleneck |
| Leveled compaction, compared with size-tiered | experiment, after the finish line |
| Incremental compaction (free inputs piece by piece instead of needing up to 2x disk) | if large merges become a disk space problem |
| Snapshots, iterators and `SCAN` | when a command needs a range read |
| RESP over QUIC, compared with TCP | experiment, after the finish line |
| A second, native protocol next to RESP | only with a concrete need RESP cannot express |
| Event loop server on `httpx` (epoll, kqueue, IOCP), compared with the Go netpoller | experiment, after the finish line |
| OpenTelemetry at request level in the server, sampled | if the server needs distributed tracing |
| Snappy compression | if disk usage matters more than CPU |
| Block cache | if a larger working set makes `ReadRandom` slow (it is 0.8x of goleveldb with 1M keys) |
