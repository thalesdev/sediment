// SPDX-License-Identifier: MIT

package shale

// defaultMemtableSize is the memtable size when Options.MemtableSize is 0.
const defaultMemtableSize = 4 << 20

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
	// refused, and the next write starts the flush.
	MemtableSize int

	// flushHook is called by the flush at the stages "table", "commit" and
	// "cleanup". If it returns an error, the flush fails. Only the tests of the
	// package set it.
	flushHook func(stage string) error
}

// memtableSize returns the size at which the memtable is frozen.
func (o Options) memtableSize() int {
	if o.MemtableSize <= 0 {
		return defaultMemtableSize
	}
	return o.MemtableSize
}
