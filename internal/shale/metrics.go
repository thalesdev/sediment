// SPDX-License-Identifier: MIT

package shale

import (
	"sync"
	"sync/atomic"
	"time"
)

// durStat counts some jobs, and keeps the time that they took in total and the time of
// the longest one.
type durStat struct {
	count atomic.Uint64
	nanos atomic.Int64
	max   atomic.Int64
}

// add counts a job that took d.
func (s *durStat) add(d time.Duration) {
	s.count.Add(1)
	s.nanos.Add(int64(d))
	for {
		cur := s.max.Load()
		if int64(d) <= cur || s.max.CompareAndSwap(cur, int64(d)) {
			return
		}
	}
}

// stallStat counts the waits of the writes for one reason, and the time that they
// took.
type stallStat struct {
	count atomic.Uint64
	nanos atomic.Int64
}

// wait waits on cond, which uses the lock of the caller, and adds the wait to s. The
// caller must hold the lock of cond.
func (s *stallStat) wait(cond *sync.Cond) {
	began := time.Now()
	cond.Wait()
	s.count.Add(1)
	s.nanos.Add(int64(time.Since(began)))
}

// Metrics is a count of the work of a DB.
type Metrics struct {
	// Tables is the number of table files that the DB reads.
	Tables int

	// DiskBytes is the size in bytes of the table files that the DB reads.
	DiskBytes uint64

	// Compactions is the number of compactions that finished.
	Compactions uint64

	// CompactionBytesRead is the number of bytes of the tables that the
	// compactions read.
	CompactionBytesRead uint64

	// CompactionBytesWritten is the number of bytes of the tables that the
	// compactions wrote.
	CompactionBytesWritten uint64

	// FlushBytes is the number of bytes of the tables that the flushes wrote.
	FlushBytes uint64

	// StallFlushCount and StallFlushNanos are the number of times that a write waited
	// because the frozen memtable was still being written to a table, and the time
	// that those waits took, in nanoseconds.
	StallFlushCount uint64
	StallFlushNanos int64

	// StallCompactionCount and StallCompactionNanos are the number of times that a
	// write waited because there were too many tables and the compaction had to catch
	// up, and the time that those waits took, in nanoseconds.
	StallCompactionCount uint64
	StallCompactionNanos int64

	// Flushes is the number of flushes that ended, and FlushNanos and CompactionNanos
	// are the time that the flushes and the compactions ran, in nanoseconds.
	Flushes         uint64
	FlushNanos      int64
	CompactionNanos int64

	// FreezeCount, FreezeNanos and FreezeMaxNanos are the number of times that a write
	// froze the memtable, the time that this took in total and the time of the longest
	// one, in nanoseconds. The write holds the lock of the DB, and no other write goes
	// on, while it syncs the log and the directory and makes a new log.
	FreezeCount    uint64
	FreezeNanos    int64
	FreezeMaxNanos int64

	// BlockCacheHits is the number of reads of a data block that found it in the block
	// cache, and BlockCacheMisses the number that did not.
	BlockCacheHits, BlockCacheMisses uint64

	// BlockCacheBytes is the size of the blocks in the block cache, with their
	// overhead.
	BlockCacheBytes int
}

// Metrics returns the counts of the DB. The write amplification is the sum of
// FlushBytes and CompactionBytesWritten, divided by the bytes that the caller
// wrote.
func (db *DB) Metrics() Metrics {
	m := Metrics{
		Compactions:            db.compactions.Load(),
		CompactionBytesRead:    db.compactRead.Load(),
		CompactionBytesWritten: db.compactWritten.Load(),
		FlushBytes:             db.flushBytes.Load(),
	}
	m.StallFlushCount, m.StallFlushNanos = db.stallFlush.count.Load(), db.stallFlush.nanos.Load()
	m.StallCompactionCount, m.StallCompactionNanos = db.stallCompaction.count.Load(), db.stallCompaction.nanos.Load()
	m.Flushes, m.FlushNanos, m.CompactionNanos = db.flushes.Load(), db.flushNanos.Load(), db.compactNanos.Load()
	m.FreezeCount, m.FreezeNanos, m.FreezeMaxNanos = db.freezes.count.Load(), db.freezes.nanos.Load(), db.freezes.max.Load()
	st := db.cache.Stats()
	m.BlockCacheHits, m.BlockCacheMisses, m.BlockCacheBytes = st.Hits, st.Misses, st.Bytes
	state := db.state.Load()
	m.Tables = len(state.tables)
	for _, t := range state.tables {
		m.DiskBytes += t.meta.Size
	}
	return m
}
