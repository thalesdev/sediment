# Benchmarks

Shale against goleveldb, with the workloads of the LevelDB `db_bench`. Both
engines use keys of 16 bytes, values of 100 bytes, a memtable of 4 MiB, a bloom
filter of 10 bits, and no compression. Neither engine syncs a write, except in the
two `Sync` rows.

Run it with `make bench-compare` (`BENCHCOUNT=10` for `benchstat`). These
benchmarks need a fixed count, so use `-benchtime=Nx`, as the Makefile does.

## How to read the comparisons with Redis

Two different questions, and the tables of this file answer them separately.

- **How much does a request cost?** The fair comparison is one core against one core, and the
  CPU per command (the section "Per core"). The measure in that section was taken before the
  skiplist without a lock and the cache of blocks, and with one core Redis spent 2 to 6 times
  less CPU per command than sediment.
- **How much does a machine give?** The comparison is the total rate with all the cores. Redis
  runs its commands on one thread, so a win of sediment over it in this sense is a win of using
  more cores (4 to 9 busy cores, against 1) and not of doing less work per request. Against Redis
  with `--io-threads 4`, which used 2.4 to 2.9 cores, sediment was behind at every pipeline depth
  in the same section.

So "sediment is faster than Redis" is true only for the first column of the second question, and
it should not be quoted alone. The strength that is real is that sediment gets more from free
cores. The weakness that is also real is the CPU per command.

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

### Against Redis 8.10.3, same machine and settings

Redis with `--save "" --appendonly no`, sediment with `Sync` off, the same
`redis-benchmark` (`-t get,set -n 500000 -r 100000 -d 100`, 50 clients).

| Workload | Redis | sediment |
|---|---:|---:|
| `SET`, 1 client thread | 59,298 | 58,370 |
| `GET`, 1 client thread | 57,958 | 59,552 |
| `SET`, 4 client threads | 66,658 | 105,219 |
| `GET`, 4 client threads | 66,658 | 117,564 |
| `SET`, 1 connection, avg latency | 0.039 ms | 0.052 ms |
| `GET`, 1 connection, avg latency | 0.039 ms | 0.050 ms |
| `SET`, 4 client threads, p99 | 0.855 ms | 2.159 ms |
| `GET`, 4 client threads, p99 | 0.839 ms | 1.415 ms |

What this says:

- With one client thread both servers give about 59,000 per second, so the first
  numbers of this file (47,000 to 52,000) were the limit of `redis-benchmark` and not
  of the server.
- With four client threads sediment does 1.6 to 1.8 times what Redis does, because it
  uses many cores and Redis runs commands on one.
- One round trip costs about 12 microseconds more than in Redis. The CPU profile puts
  67% of the time in system calls (the reply `write` is the largest) and about 25%
  in the netpoller and the scheduler. The code of the commands is 26%. The server
  used 1.2 of 11 cores while the clients pushed as hard as they could.
- The tail is worse: p99 of 1.4 to 2.2 ms against 0.85 ms, and a maximum of 12 to
  25 ms against 1 ms. The cause is not known. The garbage collector is the first
  suspect, because `sstable.readBlock` is 83% of the bytes allocated, about 4 KiB for
  each `GET` that reaches a table.
- The runs with `-P 16` are not reliable. Redis gives exactly 666,666.69 for `SET`
  and for `GET`, and sediment gives exactly 500,000.00 for `GET`. A run of 500,000
  commands ends in less than a second, and the result has a coarse resolution. They
  must be repeated with at least 5,000,000 commands.
- The profile used `-memprofilerate 1`, which adds some 5% to 8% of CPU to the
  numbers of the profile.

### After the block pool and the 64 MiB memtable

Changes: `sstable.Find` reads into a pooled buffer (ReadRandom of the engine, 2.50 to
2.24 us, -10.3%, p=0.000, 10 runs with benchstat, goleveldb control unchanged), and
the server takes a memtable of 64 MiB by default. 3,000,000 keys of 100 bytes, which
is more than the memtable, so a `GET` reads tables. `redis-benchmark`, 4 threads, 50
clients. "Before" is commit 7bbc283.

| Workload | Redis | before | after |
|---|---:|---:|---:|
| `GET`, `-P 16`, per second | 749,906 | 631,446 | 824,969 |
| `GET`, `-P 16`, p99 | 1.48 ms | 8.46 ms | 4.31 ms |
| `SET`, `-P 16`, per second | 648,578 | 413,650 | 406,724 |
| `SET`, `-P 16`, p99 | 1.97 ms | 9.38 ms | 4.98 ms |
| `SET`, `-P 16`, median | 1.14 ms | 1.34 ms | 1.84 ms |
| `GET`, no pipeline, per second | 66,664 | 127,421 | 124,938 |
| `SET`, no pipeline, per second | 65,215 | 109,051 | 111,078 |
| `SET`, no pipeline, p99 | 0.89 ms | 1.44 ms | 0.86 ms |

- A pipelined `GET` is now faster than Redis, and its p99 is half of what it was.
- Without a pipeline sediment does 1.7 to 1.9 times what Redis does, and the p99 of
  `SET` is the same as Redis.
- A pipelined `SET` did not get faster (63% of Redis), its p99 fell from 9.4 to 5.0
  ms, and its median got worse.
- The maximum latency has spikes of 20 to 70 ms in every version of sediment, and 7
  ms in Redis at most. The cause is not known.
- This comparison of `SET` favors Redis, because it ran with `--appendonly no` and
  writes nothing to disk, while sediment writes each group to its log so that a
  `kill -9` does not lose a write that returned. The fair setting for Redis is
  `--appendonly yes --appendfsync no`.

### The pipelined SET: batch, lock convoy and shared locks

The first comparison with Redis ran it with `--appendonly no`, which writes nothing.
The fair setting is `--appendonly yes --appendfsync no`: Redis appends each command
to a buffer and does one `write(2)` at the end of each turn of its event loop, and
it replies before that write. sediment replies only after the record is in the log,
so a write that returned survives `kill -9`.

With that setting, a pipelined `SET` was 81% of Redis. The commands of one connection
ran one at a time, and each waited for its own write to the log, because the group
commit joins writes of different connections only. The server now runs the plain
`SET` commands of a connection that already arrived whole as one write
(`command.SetMany`), and answers in the same order.

The first version of the batch was slower at the tail. The median fell (1.8 to 0.5
ms) and the p99 rose (4.7 to 26 ms): some connections ran and others waited. A block
profile of the two versions showed why:

| | before the batch | batch, exclusive locks |
|---|---:|---:|
| Wait in `Mutex.Lock` of the stripe locks | 37.8 s (9% of the blocked time) | 333 s (74.5%) |
| Wait in the group commit (`Cond.Wait`) | 290 s | 53 s |
| CPU used in 30 s | 90% | 66% |

The batch held up to 64 of the 256 stripe locks while it waited for the log, and the
clients waited for each other. The stripes are now read-write locks. `SET` and `MSET`
take the shared side, so they do not wait for each other and still share the group
commit. `SET NX/XX/GET` and `DEL` take the exclusive side, which waits for the shared
holders and keeps them out, so they stay atomic.

`SET` only, 3,000,000 keys of 100 bytes, `redis-benchmark` with 4 threads and 50
clients, two rounds. Redis with `--appendonly yes --appendfsync no`.

| Workload | Redis | no batch | batch, exclusive locks | batch, shared locks |
|---|---:|---:|---:|---:|
| `-P 16`, per second | 499,792 and 479,885 | 428,541 and 399,947 | 393,365 and 363,570 | **648,438 and 615,195** |
| `-P 16`, p99 | 3.50 and 3.79 ms | 4.48 and 4.82 ms | 24.9 and 26.8 ms | 3.50 and 3.74 ms |
| `-P 64`, per second | 799,467 and 773,694 | 428,449 and 420,875 | 545,058 and 521,467 | **798,722 and 705,633** |
| `-P 64`, p99 | 8.46 and 10.26 ms | 15.4 and 15.9 ms | 35.6 and 35.2 ms | 9.94 and 11.0 ms |
| no pipeline, per second | 61,851 and 61,851 | 115,349 and 105,234 | 113,156 and 107,089 | 109,051 and 109,051 |

- With `-P 16` sediment does 1.28 to 1.30 times what Redis does, and the p99 is the
  same.
- With `-P 64` it does 1.00 and 0.91 times. Two rounds are too few for a median here,
  and the spread of sediment (12%) is larger than the spread of Redis (3%).
- Without a pipeline the batch changes nothing, and sediment does 1.7 to 1.9 times
  Redis.
- Redis runs commands on one thread and sediment on many, and this is one machine, one
  value size and `SET` only. Redis was not run with `io-threads`.

### Per core: sediment spends 3 to 6 times the CPU of Redis per command

The comparisons above use all the cores of the machine for sediment (4 to 9 cores
busy) and one thread for Redis, which runs its commands on one. A server does not
always have that many cores, so the fair measure is the CPU that each server uses.
`redis-benchmark` 8.10.3, 4 client threads, 50 clients, 3,000,000 keys of 100 bytes,
5 rounds, medians. The CPU is the user and system time of the server process, read
with `ps` before and after each run. `sed-1` and `sed-2` are sediment with
`GOMAXPROCS` 1 and 2, and `redis-io4` is Redis with `--io-threads 4`. Redis has
`--appendonly yes --appendfsync no`.

| | Redis | redis-io4 | sed-1 | sed-2 | sed-all |
|---|---:|---:|---:|---:|---:|
| `GET`, no pipeline, per second | 65,931 | 124,968 | 49,583 | 109,075 | 127,605 |
| `SET`, no pipeline, per second | 62,494 | 117,628 | 39,990 | 95,226 | 115,340 |
| `GET`, `-P 16`, per second | 749,906 | 1,599,573 | 208,652 | 363,570 | 856,531 |
| `SET`, `-P 16`, per second | 489,596 | 1,090,710 | 212,006 | 406,586 | 666,444 |
| `GET`, `-P 64`, per second | 1,599,573 | 2,400,000 | 255,134 | 461,290 | 999,666 |
| `SET`, `-P 64`, per second | 773,694 | 1,043,296 | 295,624 | 533,143 | 799,786 |
| `GET`, `-P 16`, CPU us per command | 1.33 | 1.83 | 4.83 | 5.50 | 10.50 |
| `SET`, `-P 16`, CPU us per command | 1.83 | 2.17 | 4.50 | 4.33 | 6.00 |
| `GET`, `-P 64`, CPU us per command | 0.50 | 0.67 | 3.83 | 4.33 | 9.50 |
| `SET`, `-P 64`, CPU us per command | 1.17 | 1.17 | 3.33 | 3.33 | 4.33 |
| `GET`, no pipeline, cores used | 1.01 | 2.92 | 0.99 | 1.96 | 5.44 |
| `GET`, `-P 16`, cores used | 1.00 | 2.93 | 0.99 | 2.00 | 8.85 |

- With one core each, Redis does 1.3 to 1.6 times our rate without a pipeline, and 2.3
  to 6.3 times with one.
- sediment on all the cores matches Redis with `io-threads 4` only without a pipeline,
  and it uses 5.4 cores for what Redis does with 2.9. With a pipeline Redis with
  `io-threads` is ahead at every depth.
- So the earlier ratios of 1.3 to 1.9 times Redis came from using more cores, and they
  are not an efficiency.
- Cautions: the p99 column of this script is wrong and was not used. `ps` has a
  resolution of one second. `GOMAXPROCS` does not pin threads, but the cores column
  shows 0.96 to 1.00. The Redis `GET` is from memory and the sediment `GET` reads
  tables on disk for most of the 3,000,000 keys, and it is a part of the gap. The
  `io-threads` numbers with `-P 64` are probably limited by the client.

The medians of 8 rounds with all the cores, `SET` only (`batch3`): `-P 16`, Redis 499,875
and sediment 639,744; `-P 64`, Redis 799,360 and sediment 761,542.

Splitting the CPU of each server with `ps` (user and system), with one core each:

| us per command | Redis user + sys | sediment user + sys |
|---|---:|---:|
| `SET`, `-P 16` | 0.83 + 0.83 | 2.5 to 2.7 + 2.17 |
| `GET`, `-P 16` | 0.50 + 1.00 | 3.3 to 3.7 + 1.5 to 1.7 |
| `SET`, `-P 64` | 0.83 + 0.17 | 2.2 to 2.3 + 1.0 |
| `GET`, `-P 64` | 0.33 + 0.33 | 2.8 to 3.2 + 1.17 |
| `PING`, `-P 16` | 0.17 + 0.83 | 0.17 + 0.83 |

- A `PING`, which uses no storage, costs the same in both. The network layer of Go is
  not the problem.
- The extra cost is in the user CPU of `SET` and `GET`, 3 to 9 times that of Redis. The
  garbage collector is 4% to 8% of the CPU.
- The CPU profile of Go on macOS puts 70% to 92% of the time in `syscall` and cannot be
  trusted. The benchmark without the network (`make bench-inproc`) has no socket
  system calls, and the times in it do not depend on the profiler: the server layer
  costs 0.17 to 0.31 us per command, the engine 1.15 us for a `SET`, 0.53 us for a
  `GET` from the memtable and 2.2 us for a `GET` from a table. The `pread` is 1.7 us of
  the `GET` from a table.

### What a skiplist in an arena with no lock can win

Two ceilings, measured before writing it (Apple M3 Pro, 11 cores).

- The lock on reads. Seeks in a skiplist of 100,000 keys with 11 goroutines take about
  120 ns each (wall time) with the `RWMutex` of the iterator, and about 55 ns with no lock
  at all (`BenchmarkSeekParallel` and `BenchmarkSeekParallelNoLock`, nobody writes in the
  second one so it is safe). One goroutine takes 345 ns, so with the lock the reads scale
  3.1 times on 11 cores, and without it 6.3 times.
- The garbage collector on writes. A `SET` through the connection loop with a memtable of
  64 MiB and one core (`go test -cpu 1`, `BenchmarkInprocSetShale`) takes 2,265 to 2,365 ns
  with `GOGC=100`, about 1,900 ns with `GOGC=400` and 1,763 to 1,895 ns with `GOGC=off`.
  The collector is about 19% of the time of a write. With 11 cores it runs on other cores
  and the wall time does not show it (1,521 to 1,668 ns against 1,448 to 1,667 ns).

### The skiplist in blocks without pointers and without a lock

The memtable is now a `skiplist.List`: the nodes are indexes in a `[]uint32`, the keys and
the values are in a `[]byte`, both in blocks of 1 MiB that have no pointers, and an insert
links a node with a compare and swap at each level. There is no `unsafe` and no lock.
10 runs of each, `benchstat`, Apple M3 Pro, against the skiplist with a lock (commit
`bbd0096`). The control, goleveldb, ran in the same two sessions.

| Benchmark | with a lock | no lock, in blocks | |
|---|---:|---:|---|
| `SET` through the connection loop, one core | 2.455 us | 1.691 us | -31% |
| `SET` through the connection loop, 11 cores | 1.661 us | 1.374 us | -17% |
| `GET` from the memtable, through the connection loop | 780 ns | 665 ns | -15% |
| `Seek`, one goroutine | 348 ns | 348 ns | same (p=0.47) |
| `Seek`, 11 goroutines | 110 ns | 44.7 ns | -59% |
| `Iterator.Next` | 14.4 ns | 10.3 ns | -29% |
| `Insert`, one goroutine | 369 ns | 427 ns | +16% |
| Allocations and bytes per `Insert` | 2 and 109 B | 0 and 66 B | |
| `FillRandom` of the engine | 5.70 us | 5.36 us | -6% (goleveldb -2%) |
| `FillRandomParallel` of the engine | 3.31 us | 3.09 us | -7% (goleveldb same) |

How the two losses were found and fixed:

- The first version made `Seek` 19% slower. A profile put 27% of the time in the decoding of a
  node index, and an experiment showed the cause: each step did two atomic loads of a block
  pointer. An atomic load cannot be moved past the loads after it, so the processor could not
  overlap the memory latency of the steps. The tables of block pointers are written one time,
  before the node that uses them is linked, and a reader reads a slot only after an atomic load
  of a link that was stored after it, so they are now plain arrays. Taking out the bounds checks
  alone gave 3%, and the plain tables gave the rest.
- `Insert` is 16% slower with one goroutine. The profile puts 64% of it in the search, which
  now costs the same as `Seek`, and about 10% in the new node, the copy of the key and the
  value and the compare and swap operations. The `Insert` benchmark does not show the saving
  of the garbage collector, and the engine does: the collector was about 19% of a write on one core.
- The concurrent tests found one bug that the lock used to hide: `Seek` read the link of the
  last node again after the loop, and an insert in between could give it a key smaller than the
  one it looked for.
- The write amplification of the engine fell from 3.54 to 2.93 and from 3.37 to 2.93. It is not
  a gain of the skiplist: the old size estimate counted about 100 bytes of structure for each
  node, the new one counts 24 to 40, so a memtable of 4 MiB holds more entries, the tables are
  larger, and there are fewer compactions. The memory that a memtable really uses was not
  measured, and it is not the same as before for the same limit.

### The cache of data blocks

`sstable.BlockCache` keeps the data blocks that `Find` reads: 16 parts with a lock each, a
limit in bytes, the least recently used block dropped first, and the blocks of a table dropped
when the engine removes the file. All the tables of a DB share one cache. `shale.Options.BlockCacheSize`
is 8 MiB by default (as in LevelDB and goleveldb) and a negative value turns it off, and `sedimentd`
has `block-cache-size`, 64 MiB by default. `INFO` shows `block_cache_hits`, `block_cache_misses` and
`block_cache_bytes`.

A `Find` with the source in memory (no system calls, so the CPU profile and the times do not depend
on the operating system), `BenchmarkFind*` in `internal/sstable`:

| `Find` | Time | Bytes |
|---|---:|---:|
| no cache | 906 to 930 ns | 304 |
| a cache with 5% of the blocks, almost every read a miss | 916 to 919 ns | 890 |
| a hit | 466 to 482 ns (-48%) | 309 |

The first version of the cache made the miss 17% slower (1,090 ns and 5,168 bytes), because each
miss allocated a block of 4 KiB and took the lock to add it, and the benchmark with a cache much
smaller than the blocks in use showed it. A part of the cache that is full now adds one miss in
eight, chosen at random, and reads the others into the pooled buffer. A part that has room adds
every block, so a set of hot blocks that fits is in the cache at once.

A `GET` that reaches a table, through the connection loop, 11 cores, 6 rounds alternating the build
without the cache and the build with it in the same session (benchstat, goleveldb control 2.949 and
2.945 us):

| Workload | no cache | 8 MiB (default) | cache that holds everything |
|---|---:|---:|---:|
| 1,000,000 keys at random (130 MB, the cache has 6% of it) | 2.172 us | 2.265 us (+4%, noise) | 1.147 us (-47%) |
| 1,000,000 keys at random, 50 connections | 783 ns | | 283 ns (-64%) |
| **hot set of 50,000 keys** | 1.754 us | **856 ns (-51%)** | |
| **hot set of 50,000 keys, 50 connections** | 891 ns | **284 ns (-68%)** | |
| `ReadRandom` of the engine, 1,000,000 keys at random | 1.995 us | 1.984 us (same, p=0.70) | |

- With the default cache a hot set is read twice as fast with one connection and three times
  as fast with 50, which is the use of a service of flags.
- A cache much smaller than the data with uniform reads costs nothing now (+4%, inside the noise),
  and the path with the cache off did not change (2.142 and 2.172 us, p=0.07).
- Not measured: the `GET` through the network with `redis-benchmark`, and the memory of a cache of
  64 MiB in a server with many connections.

### The pipelined SET after the skiplist and the cache, all the cores

`redis-benchmark`, 4 threads, 50 clients, 3,000,000 keys of 100 bytes, 6 rounds that alternate
Redis (one thread, `--appendonly yes --appendfsync no`) and sediment (commit `1627bb3`, all the
cores), `SET` only. Medians of the rounds. The p99 is the seventh column of the lines of each round.

| `SET` | Redis | sediment | |
|---|---:|---:|---|
| `-P 16`, per second | 484,701 | 666,444 | 1.37 times (before the two changes 1.28) |
| `-P 16`, p99 | 6.82 ms | 2.13 ms | sediment was 3.66 ms before |
| `-P 64`, per second | 780,415 | 914,713 | 1.17 times (before 0.95) |
| `-P 64`, p99 | 11.29 ms | 10.65 ms | sediment was 11.53 ms before |

- The p99 of Redis in this session (6.82 and 11.29 ms) is higher than in the session before
  (3.60 and 7.99 ms), with the same version and the same settings, so the machine was in a
  worse state. The comparison inside a session is fair, and the number of Redis alone is not
  a constant.
- The spread of sediment with `-P 64` is large, 761,000 to 1,000,000 (31%) against 761,000 to
  800,000 (5%) for Redis, and in the worst round the two are equal (761,469 and 761,179).
- This is the win of using many cores that the box "How to read the comparisons with Redis" says.
  The CPU per command with one core was not measured again after the two changes.

### Per core and with all the cores, after the skiplist and the cache

The same `bench5` as in the section "Per core", on commit `950a783`, 3 rounds, medians.
`redis-benchmark`, 4 threads, 50 clients, 3,000,000 keys of 100 bytes. `us/cmd` is the CPU of the
server per command. The p99 is the median of the lines of the rounds, because the p99 column
of the summary of the script is wrong.

CPU per command with one core (`GOMAXPROCS=1`), against Redis with one thread, and the change from
the measure before the skiplist without a lock and the cache of blocks:

| us per command | Redis | sediment, one core | before | rate of sediment | p99 of sediment | p99 of Redis |
|---|---:|---:|---:|---:|---:|---:|
| `SET`, `-P 16` | 1.83 | 3.83 | 4.50 | 252,429 | 33.0 ms | 6.2 ms |
| `GET`, `-P 16` | 1.17 | 3.83 | 4.83 | 252,589 | 5.6 ms | 1.4 ms |
| `SET`, `-P 64` | 1.00 | 2.50 | 3.33 | 368,799 | 35.9 ms | 11.6 ms |
| `GET`, `-P 64` | 0.67 | 3.00 | 3.83 | 324,219 | 17.5 ms | 3.0 ms |
| `SET`, no pipeline | 15.33 | 22.00 | 24.67 | 44,770 | 1.9 ms | 0.9 ms |
| `GET`, no pipeline | 14.67 | 18.67 | 20.00 | 52,627 | 1.6 ms | 0.9 ms |

- The CPU per command fell by 15% to 25% in every case, and the rate with one core rose by 12% to 27%.
  The gap to Redis is still 1.4 to 4.5 times in CPU per command, and it is largest for `GET` with a pipeline.
- With one core the p99 of a pipelined `SET` is 33 to 36 ms against 6 to 12 ms for Redis. The flush and the
  compaction compete with the requests for the one core and have no priority over them. With all the cores the
  p99 of `SET` with `-P 16` is 1.9 ms.

Total rate with all the cores, against Redis with one thread and with `--io-threads 4`:

| per second | sediment, all cores | Redis | ratio | Redis `io-threads 4` | ratio |
|---|---:|---:|---:|---:|---:|
| `SET`, no pipeline | 109,059 | 63,824 | 1.71 | 105,218 | 1.04 |
| `GET`, no pipeline | 117,600 | 68,178 | 1.72 | 111,086 | 1.06 |
| `SET`, `-P 16` | 705,716 | 499,708 | 1.41 | 999,666 | 0.71 |
| `GET`, `-P 16` | 825,763 | 774,093 | 1.07 | 1,411,432 | 0.59 |
| `SET`, `-P 64` | 922,793 | 799,573 | 1.15 | 1,043,115 | 0.88 |
| `GET`, `-P 64` | 1,089,918 | 1,599,573 | 0.68 | 2,666,666 | 0.41 |

- Against Redis with one thread sediment is ahead for `SET` at every depth and for everything without a pipeline.
- Against Redis with `io-threads 4` it only ties without a pipeline (1.04 and 1.06). With a pipeline Redis is ahead
  at every depth, with 2.3 to 2.9 busy cores against 3 to 9 for sediment.
- `GET` with `-P 64` uses 8.7 cores and 8 us per command, almost all of it reads of cold blocks (3,000,000 keys,
  cache of 64 MiB). It is the weakest case.

### The p99 of a write with one core

With `GOMAXPROCS=1` the p99 of a pipelined `SET` (`-P 16`, 50 clients) was 26 to 28 ms and the maximum
66 to 82 ms, against 2.3 ms with all the cores. `INFO` now shows what was needed to find the cause:
the waits of the writes for a flush and for a compaction, the time that the flushes and the
compactions run, the time that a freeze of the memtable holds the lock, and, from the runtime of Go,
the wait of a goroutine to run (`sched_latency_*`) and the pauses of the garbage collector (`gc_pause_*`).

What each measure ruled in or out:

| Guess | Measure | Result |
|---|---|---|
| The writes wait for the flush or the compaction | `write_stalls_flush` and `write_stalls_compaction` | 0 and 0, ruled out |
| The garbage collector | `gc_pause_max_ms` | 0.08 ms, ruled out (and `GOGC=off` took the p99 from 7.6 to 7.0 ms) |
| The freeze of the memtable holds the lock | `freeze_max_ms` | 9 ms in 8 freezes, and the same with all the cores, ruled out |
| A goroutine waits for the processor | `sched_latency_p99_ms` | 14.7 ms with one core against 0.23 ms with 11: a part of it |
| The requests wait for the network | with `runtime.Gosched` in the jobs, the wait for the processor fell to 2.6 ms and the p99 stayed at 24.7 ms | the cause |

The cause is in the scheduler of Go, and it only exists with one processor. It looks for network events
only when its queues of goroutines are empty, or every 10 ms from another thread. The flush and the compaction
are code that never waits, and a job that calls `runtime.Gosched` goes back to the queue and keeps it from being
empty, so a connection whose bytes already came waits until the thread of the scheduler looks. The wait is not in
`sched_latency`, because the goroutine was not ready yet. With two processors one of them is idle and looks by itself
(`sed-2` had a p99 of 4.1 ms in the earlier measure without any change).

The fix is a pause of 20 microseconds every 128 entries in the flush and in the compaction, only when the program has
one processor (`yielder` in `internal/shale/yield.go`). A sleep takes the job out of the queue and the processor looks for
the network events. 1 core, `SET -P 16`, 3,000,000 commands, one run each of the other settings, and 4 rounds that
alternate the build without and with the change for the one that was kept:

| Pause | p99 | p95 | flush, all of them | compaction, all of them | rate |
|---|---:|---:|---:|---:|---:|
| none | 28.0 ms | 6.5 | 2.5 s | 2.0 s | 213,000 |
| `Gosched` every 64 | 24.7 ms | 8.2 | 2.4 s | 1.9 s | 239,000 |
| 20 us every 64 | 10.8 ms | 7.0 | 5.7 s | 4.6 s | 216,000 |
| **20 us every 128** | **9.95 ms** | 8.2 | 3.6 s | 3.1 s | 235,000 |
| 20 us every 256 | 15.2 ms | 10.6 | 2.9 s | 2.5 s | 219,000 |
| 50 us every 64 | 8.3 ms | 6.6 | 7.8 s | 6.5 s | 222,000 |
| 100 us every 64 | 9.0 ms | 6.1 | 11.0 s | 3.9 s | 222,000 |

| 4 rounds, one core | p99 | maximum | p95 | rate |
|---|---:|---:|---:|---:|
| without the change | 25.8 to 27.7 ms | 66 to 82 ms | 4.7 to 5.7 ms | 231,000 to 245,000 |
| with the change | **9.7 to 11.0 ms** | **22 to 30 ms** | 7.7 to 8.2 ms | 240,000 to 250,000 |

- The p99 fell by 63% and the maximum by about 65%, with the same rate. The floor without any flush is about 7 ms, which
  is the queue of 50 connections with a pipeline of 16 on one core.
- The cost: the p95 rose from about 5 to about 8 ms, and the p50 from 2.6 to 2.8 ms, because the jobs take longer on the clock
  (a flush 40% longer, a compaction 55% longer) and touch more requests, each one with a smaller delay.
- With 2 and with 11 cores the change does nothing, and the measures did not show a systematic difference. With 2 cores the
  mean rate was 4% lower in the build with the change over 7 rounds, which is one step of the resolution of `redis-benchmark`, and
  it was not in the other two measures.
- Not tested: a real machine with one CPU on Linux (a container with a limit of one CPU makes Go use one processor), and
  with a pipeline of 64 or with `GET`.

#### The p95 with one core: what was tried

The pause of 20 us every 128 entries took the p99 from 27 to 10 ms and raised the p95 from about 5 to
about 8 ms. Variants of the pause, one core, `SET`, 50 clients, 2 to 3 runs each, in ms. A "time" slice
pauses after that much continuous work (checked every 16 entries), and "adaptive" does not pause when the
memtable is 75% full or the tables are 75% of the limit that stops the writes:

| | `-P 1` p95 / p99 | `-P 16` p95 / p99 / max | `-P 64` p95 / p99 |
|---|---:|---:|---:|
| no pause | 1.9 / 2.0 to 2.5 | 4.4 / 26 / 58 to 68 | 27 / 31 to 34 |
| **kept**: count 128, 20 us | 1.9 to 2.0 / 3.0 to 3.2 | 7.7 / 9.7 to 9.9 / 25 to 36 | **21 to 22 / 29** |
| adaptive, time 40 us, 20 us | 1.95 to 2.02 / 3.0 to 3.2 | **6.5 to 6.7 / 7.8 to 8.0 / 18 to 19** | 25 / 31 |
| adaptive, time 25 us, 20 us | | 5.9 to 6.0 / 7.2 to 8.1 / 41 to 43 | 26 / 31 |
| count 32, 50 us (fixed) | | 5.0 to 5.4 / 6.3 to 6.8 / **243 to 276** | not run |

- The pause that is best for a pipeline of 16 (time slices of 40 us, adaptive) is 15% better in p95 and 19% better
  in p99 there, the same without a pipeline, and 15% worse in p95 with a pipeline of 64, where the queue of 50
  connections with 64 commands each decides the latency (a p50 of 8 ms without any flush). No setting wins in the three.
- The settings with the smallest p95 let the flush fall behind: the count 32 and 50 us setting stalled the writes
  (one stall, and maxima of 243 to 276 ms), and fixed time slices of 25 us with 20 or 30 us had 5 and 13 stalls with a
  pipeline of 64. The adaptive rule (no pause when the memtable is 75% full) removed the stalls in every run.
- The count 128 and 20 us setting was kept: it has no stalls in any run, it is the simplest, and it is the best with
  a pipeline of 64. The adaptive version with time slices is a candidate if one core with a pipeline of about 16 is the
  case that matters.


### The key layout of the server

The keys of the server got a class and a hash slot (3 bytes), and the value got a header
with its type and an optional deadline. A command now computes the CRC-16 of its key, and
allocates the engine key, and the keys of the memtable are in the order of the slot and no
more in the order of the user key. Measured with the same benchmarks, before and after,
median of 3 runs of 1 second, 11 cores:

| Benchmark | Before | After |
|---|---:|---:|
| `GET`, server layer only (a store that does nothing) | 167 ns | 225 ns |
| `SET`, server layer only | 326 ns | 378 ns |
| `GET` from the memtable, in process | 680 ns | 850 ns |
| `SET` through Shale, in process | 1,430 ns | 1,650 ns |
| `GET` through the connection loop and the network | 10.3 us | 10.4 us |
| `SET` with a pipeline of 16, through the network | 1.98 us | 2.03 us |

The server layer pays about 50 ns for each command, and the engine pays 18% to 25% in
process, because the keys no longer arrive in a pattern that the skip list likes. Through the
network, which is what a client sees, the difference is inside the noise. The in-process
`GET` of a table is too noisy to give a number (2.3 us before, 2.7 to 4.1 us after).
Not tried, because the rule is to measure before an optimization: a stack buffer for the
key, which the `Store` interface would still make escape, and a slot table.

### Milestone 2: the cost of the timing, and the first numbers on Linux

**The timing of each command.** Every command passes one place, `Server.observe`, where it
is timed for the slow log, the metrics and the traces. `BenchmarkInprocGetNopStore` and
`BenchmarkInprocSetNopStore` (the server layer with no network and no engine, 10 runs, Apple
M3 Pro, `benchstat`) before and after the slow log, the time of the last command of a client
and the sign in checks:

| Benchmark | Before | After |
|---|---|---|
| `GET`, server layer only | 238 ns | 287 ns (+21%) |
| `SET`, server layer only | 371 ns | 397 ns (+7%) |

The allocations did not change (6 and 9). The two calls of `time.Now` are the cost, and the
metrics and the traces need the same time, so they add none. With no `Meter` and no `Tracer`
the server keeps nothing more. Through the network the 50 ns are inside the noise.

**CPU per command in a container with one CPU, on Linux.** `scripts/measure-linux.sh`
runs the image of `make docker` with `--cpus=1`, and drives it with `redis-benchmark` of the
image `redis:7-alpine` in the network of the server container, 50 clients, 100 byte values,
100,000 keys, 500,000 requests. The CPU is the user plus system time of the server (`INFO cpu`),
divided by the number of requests.

| Workload | 2 Ps: requests/s | 2 Ps: us of CPU | 1 P: requests/s | 1 P: us of CPU |
|---|---|---|---|---|
| `SET` | 133,000 | 7.4 | 143,000 | 6.8 |
| `GET` | 165,000 | 6.0 | 174,000 | 5.7 |
| `SET`, pipeline 16 | 440,000 | 2.3 | 363,000 | 2.7 |
| `GET`, pipeline 16 | 498,000 | 2.1 | 403,000 | 2.5 |
| `SET`, pipeline 64 | 538,000 | 1.9 | 480,000 | 2.1 |

Two things the numbers do not say. The Go runtime sets `GOMAXPROCS` from the limit of the
container but never below 2, so a limit of 1 CPU runs with 2 Ps (the first four columns) unless
`GOMAXPROCS=1` says otherwise. And this is a Linux virtual machine of a laptop, with 2 vCPUs
that the load generator shares with the server, on an arm64 Apple M3 Pro. It shows that the
script and the image work and the order of the cost, which is 6 to 7 us of CPU for a `SET` or
a `GET` with no pipeline, and 2 us with a pipeline of 16 or more, as on macOS in
[Per core](#per-core-sediment-spends-3-to-6-times-the-cpu-of-redis-per-command). It is not a number
for a real deployment: run the script on the machine and the limit of the deployment before
you say one.
