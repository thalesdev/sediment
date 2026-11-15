// SPDX-License-Identifier: MIT

package shale

// defaultMemtableSize is the memtable size when Options.MemtableSize is 0.
const defaultMemtableSize = 4 << 20

// maxMemtableSize is the largest memtable size. A memtable keeps its nodes, keys and
// values in 4 GiB of blocks at most, and a larger limit could leave a write with no
// room after it is in the log.
const maxMemtableSize = 1 << 30

// Options sets the parameters of a DB. The zero value is valid.
type Options struct {
	// Sync makes each write flush the write-ahead log to stable storage
	// before the write returns. The default is false.
	//
	// If Sync is false, a write that has returned is in the operating system
	// and survives the death of the process, for example a kill with SIGKILL.
	// It can be lost if the machine loses power. If Sync is true, the write
	// also survives a loss of power, and each write is much slower.
	Sync bool

	// MemtableSize is the size in bytes at which the DB freezes the memtable
	// and writes it to a table in the background. The default is 4 MiB. The
	// size is an estimate. A write that makes the memtable larger is not
	// refused, and the next write starts the flush. The largest size is 1 GiB.
	MemtableSize int

	// BlockCacheSize is the size in bytes of the cache of the data blocks of the tables,
	// which all the tables of the DB share. The default is 8 MiB. A negative value
	// turns the cache off. A cache that is large enough for the blocks that the reads
	// use saves the read of the file, the check of the checksum and the allocation of
	// the block for each Get that reaches a table.
	BlockCacheSize int

	// CompactionFilter is called by a compaction for the newest version of each
	// key when that version is a put. If it returns true, the compaction drops
	// the key, so that it reads as deleted, and an older version of the key does
	// not come back. The default is nil, which keeps every key.
	//
	// It runs in the goroutine of the compaction, so it must be fast and must not
	// call the DB. It must give the same answer for the same key and value, or
	// only change from false to true as time goes by, and it must not keep the
	// slices that it gets.
	CompactionFilter func(key, value []byte) bool

	// flushHook is called by the flush at the stages "table", "commit" and
	// "cleanup". If it returns an error, the flush fails. Only the tests of the
	// package set it.
	flushHook func(stage string) error

	// compactHook is called by the compaction at the stages "start", "output",
	// "commit" and "done". If it returns an error, the compaction fails. Only the
	// tests of the package set it.
	compactHook func(stage string) error

	// getHook is called by Get at the stage "state", after it holds the state
	// and before it reads the sequence number. Only the tests of the package set
	// it.
	getHook func(stage string)

	// iterHook is called when a snapshot or an iterator holds the state and has
	// not read the sequence number yet. Only the tests of the package set it.
	iterHook func(stage string)

	// disableCompaction stops the compaction. Only the tests of the package set
	// it.
	disableCompaction bool
}

// defaultBlockCacheSize is the size of the block cache when Options.BlockCacheSize is 0.
const defaultBlockCacheSize = 8 << 20

// blockCacheSize returns the size of the block cache, or 0 if it is off.
func (o Options) blockCacheSize() int {
	switch {
	case o.BlockCacheSize < 0:
		return 0
	case o.BlockCacheSize == 0:
		return defaultBlockCacheSize
	}
	return o.BlockCacheSize
}

// memtableSize returns the size at which the memtable is frozen.
func (o Options) memtableSize() int {
	if o.MemtableSize <= 0 {
		return defaultMemtableSize
	}
	return min(o.MemtableSize, maxMemtableSize)
}
