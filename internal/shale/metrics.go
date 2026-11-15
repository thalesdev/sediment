// SPDX-License-Identifier: MIT

package shale

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
	st := db.cache.Stats()
	m.BlockCacheHits, m.BlockCacheMisses, m.BlockCacheBytes = st.Hits, st.Misses, st.Bytes
	state := db.state.Load()
	m.Tables = len(state.tables)
	for _, t := range state.tables {
		m.DiskBytes += t.meta.Size
	}
	return m
}
