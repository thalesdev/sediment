# Benchmarks

Shale against goleveldb, with the workloads of the LevelDB `db_bench`. Both
engines use keys of 16 bytes, values of 100 bytes, a memtable of 4 MiB, a bloom
filter of 10 bits, and no compression. Neither engine syncs a write, except in the
two `Sync` rows.

Run it with `make bench-compare` (`BENCHCOUNT=10` for `benchstat`). These
benchmarks need a fixed count, so use `-benchtime=Nx`, as the Makefile does.

## Machine

Apple M3 Pro, 11 cores, 18 GiB, SSD (APFS), macOS 27.2, Go 1.25.6. One `write(2)`
of 132 bytes costs 3.6 µs on this machine, and one `fsync` costs about 2.8 ms.

## Result, after the optimizations

Median of 3 runs, 1M operations. µs per operation, lower is better.

| Workload | Shale | goleveldb | Shale / goleveldb | Write amplification (Shale / goleveldb) |
|---|---:|---:|---:|---:|
| FillRandom, 1M writes | 5.73 | 5.61 | 1.02 | 3.54 / 3.71 |
| Overwrite, 1M writes on 1M keys | 5.75 | 6.57 | 0.88 | 3.95 / 7.33 |
| ReadRandom, 1M reads on 1M keys | 2.46 | 3.07 | 0.80 | |
| FillSync, 1000 writes | 2819 | 2821 | 1.00 | |
| FillRandomParallel, 8 goroutines, 1M writes | 3.41 | 3.52 | 0.97 | 3.37 / 3.40 |
| FillSyncParallel, 8 goroutines, 4000 writes | 635 | 643 | 0.99 | |

## The steps

Each row is one change, measured with `benchstat` on 10 runs of 200,000
operations, Shale only. The numbers are the median of the "before" and of the
"after" of that change.

| Step | Benchmark | Before | After | Change |
|---|---|---:|---:|---:|
| 1. Check the restart points of the index one time, in `Open`, and not at each `Find` | ReadRandom | 4.72 µs | 2.43 µs | -48.6% |
| 2. One `write` for each record of the log | FillRandom | 9.18 µs | 5.40 µs | -41.2% |
| | Overwrite | 9.67 µs | 5.65 µs | -41.5% |
| | FillRandomParallel | 12.01 µs | 7.19 µs | -40.1% |
| 3. Fewer allocations: a key buffer that the DB reuses, and one block of memory for the key and the value of a skip list node, with the next pointers in the node | FillRandom allocations | 7 per write | 3 per write | -57% |
| | FillRandom bytes | 460 B | 398 B | -13% |
| | FillRandom time | 5.40 µs | 5.24 µs | -2.9% |
| 4. Group commit | FillRandomParallel | 7.03 µs | 3.01 µs | -57.3% |
| | FillSyncParallel | 2250 µs | 602 µs | -73.2% |

Notes:
- The gain of step 1 grows with the size of the table. With 1M keys the index of
  the biggest table has about 27,000 restart points, and ReadRandom went from
  24.5 µs to 2.46 µs.
- Step 3 changes the time by 3%. It is kept because it cuts more than half of
  the allocations and the garbage of the write path, but it is not where the
  time was.
- Group commit leaves the benchmarks of one writer as they were, within 2%.
- With step 2, a `kill -9` no longer tears a fragment between its header and its
  payload. Before, the recovery after a kill often skipped 7 bytes. Now
  `Recovery().DroppedBytes` is 0 in every round of the `kill -9` tests.

## Baseline, before any optimization

Median of 3 runs. µs per operation, lower is better.

| Workload | Shale | goleveldb | Shale / goleveldb | Write amplification (Shale / goleveldb) |
|---|---:|---:|---:|---:|
| FillRandom, 1M writes | 9.22 | 5.75 | 1.60 | 3.54 / 3.70 |
| Overwrite, 1M writes on 1M keys | 9.95 | 6.77 | 1.47 | 3.88 / 7.35 |
| ReadRandom, 1M reads on 1M keys | 24.5 | 3.42 | 7.2 | |
| FillSync, 1000 writes | 2805 | 2800 | 1.00 | |
| FillRandomParallel, 8 goroutines, 1M writes | 12.9 | 3.47 | 3.7 | 3.54 / 3.41 |
| FillSyncParallel, 8 goroutines, 4000 writes | 2936 | 603 | 4.9 | |

Write amplification is the bytes of the tables that the flushes and the
compactions wrote, divided by the bytes that the benchmark wrote. It does not
count the log. goleveldb is measured by a wrapper of its storage.

## Where the time went (Shale, before optimizing)

### Writes (FillRandom): 7 allocations and 754 bytes per write

CPU, by cumulative path:

| Share | Path |
|---:|---|
| 75.4% | `DB.Put` > `DB.write` > `wal.Writer.AddRecord` > `writeFragment` > `os.File.Write` > `syscall.write` |
| 12.2% | `DB.compactLoop` > `compact` > `writeCompacted` |
| 5.5% | `DB.flushLoop` > `flush` > `writeTable` |
| 2.0% | `merge.Iterator.Next` (compaction) |
| 2.0% | `sstable.Iterator.Next` > `openBlock` (compaction) |
| 3.3% | the runtime: `pthread_cond_wait`, `usleep`, `pthread_cond_signal`, `pthread_kill`, GC |

`wal.Writer` makes two `write` calls per record, one for the header of 7 bytes
and one for the payload. A test program on this machine measured 7.3 µs for the
two writes and 3.6 µs for one write of the same bytes. So the two calls are about
80% of the 9.2 µs of a write, and one call per record leaves about 5.6 µs.

Allocations per write, from the memory profile:

| Share of objects | Where |
|---:|---|
| 30% | `bytes.Clone` in `skiplist.LockedList.Insert` (the key and the value) |
| 27% | `skiplist.LockedList.Insert` (the node and its `next` slice) |
| 29% | `ikey.Append` (a first allocation for the user key, and a second one when the trailer grows the slice) |
| 9% | `benchKey`, which is the benchmark and not the engine |

### Reads (ReadRandom): 5373 bytes and 8 allocations per read

The CPU profile of the macOS binary puts 65% of the time in `pread`. This is
wrong, because the profile of a process on macOS gives the samples to the system
call. A timer around `ReadAt` shows 1.1 µs per call, with 0.99 calls per read, and
a read takes 24 µs.

The real cost is `sstable.newBlockIter`. `Reader.Find` makes a new iterator for the
index block on each call, and the constructor checks every restart point of the
block. After 1M writes the biggest table has 103 MB and an index of about 27,000
entries, with one restart for each entry. A test of the constructor alone takes
22.7 µs for 27,000 restarts, which is almost all of the 24 µs. The state after
`WaitIdle` is 4 tables (one of 103 MB and three of 2 MB), and the filter makes
the reads of the other tables rare.

## Server (`sedimentd`)

`make bench-server`. The client and the server run in the same process, with 11
client connections, on loopback, over a database of 100,000 keys of 16 bytes with
values of 100 bytes. The server runs with `Sync` off, so a `SET` does not wait for
`fsync`, and with `Sync` on the `SET` numbers would be much worse. The time is the
wall time per command across all the connections. The client and the server
compete for the same CPU, so each number measures both, and the real cost of the
server alone is lower. These numbers are a first measure, not a comparison.
The measure that counts is the one below, with `redis-benchmark` in its own process.

| Workload | ns/op | Commands per second | Allocations per command |
|---|---:|---:|---:|
| `GET` | 13074 | 76,500 | 17 |
| `GET`, pipeline of 16 | 1851 | 540,000 | 17 |
| `SET` | 16956 | 59,000 | 14 |
| `SET`, pipeline of 16 | 4787 | 209,000 | 14 |

The pipeline is 7 times faster for `GET`, because a round trip on loopback costs more
than the work of one command. A `GET` allocates about 4.9 KiB. This is
close to the 5373 bytes of a Shale `ReadRandom`, which measures the engine alone
with no server, so the comparison holds: the server adds little, and the cost is
probably the data block
that a table read allocates, which a block cache would remove. This was not
profiled, and nothing was optimized yet.

The pipeline helps `SET` much less than `GET` (209,000 against 540,000 per second).
A likely cause is that the commands of one connection run one at a time, and each
waits for its own write to the log, while the group commit only joins writes of
different connections. This was not measured. If it is the cause, the server could
run the consecutive `SET` commands that already arrived on a connection as one
batch and reply in the same order.

### With `redis-benchmark`

`redis-benchmark` 8.10.3 in its own process, `-t get,set -n 1000000 -r 100000 -d 100`
(50 clients, values of 100 bytes, 100,000 keys), `Sync` off, same machine, on power
and not in low power mode. Latency is in milliseconds.

| Workload | Commands per second | avg | p50 | p95 | p99 | max |
|---|---:|---:|---:|---:|---:|---:|
| `SET` | 46,926 | 0.578 | 0.551 | 0.775 | 1.087 | 10.0 |
| `GET` | 52,502 | 0.508 | 0.495 | 0.711 | 0.823 | 23.3 |
| `SET`, `-P 16` | 413,223 | 1.903 | 1.263 | 7.431 | 9.607 | 10.9 |
| `GET`, `-P 16` | 678,426 | 0.972 | 0.639 | 2.687 | 4.575 | 13.2 |

With the client in another process, the pipelined `SET` doubles (209,000 to 413,000)
and the gap to `GET` shrinks from 2.6 times to 1.6, so the shared CPU was a large part
of the first result, and the guess about one write at a time per connection matters
less than it looked. Without a pipeline the server does about 50,000 commands per
second, which is 0.5 ms per command for each of 50 clients. That is slow for loopback
and the cause is not known, so it is the first thing to profile. The p99 of the
pipelined runs is 4.6 and 9.6 ms, which is queueing behind 16 commands of 50 clients.

The same run did `SET`, `GET`, `DEL`, `MSET`, `MGET` and `HELLO 3` with `redis-cli`
8.10.3, wrote 200,000 keys with `redis-benchmark`, killed the server with `kill -9`
and read a key written just before the kill after the restart.

Next steps, in this order, each with its before and after numbers: profile the
server without a pipeline, and only then the block pool and the pipeline batch.
