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
