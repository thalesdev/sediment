// SPDX-License-Identifier: MIT

package shale

// Iterator iterates over key-value pairs in ascending key order, as defined by the
// comparator of the iterator.
// Memtables, on-disk tables and merged views all implement it.
// A new Iterator has no position. The caller must call SeekToFirst or Seek before
// any other method. The result of any use after Close is undefined.
type Iterator interface {
	// SeekToFirst moves the iterator to the first key-value pair.
	SeekToFirst()

	// Seek moves the iterator to the first key-value pair with a key that is greater
	// than or equal to key.
	Seek(key []byte)

	// Next moves the iterator to the next key-value pair. If there is no next pair,
	// Valid returns false.
	// The result of Next is undefined if Valid returns false.
	Next()

	// Valid returns true if the iterator is at a key-value pair. It returns false at
	// the end of the data and after an error. Use Err to find the cause.
	Valid() bool

	// Key returns the key of the current pair. Call it only if Valid returns true.
	// The slice is valid only until the next call to SeekToFirst, Seek, Next or
	// Close. The caller must not change it. To keep the key, the caller must copy it.
	Key() []byte

	// Value returns the value of the current pair. Call it only if Valid returns true.
	// The slice is valid only until the next call to SeekToFirst, Seek, Next or
	// Close. The caller must not change it. To keep the value, the caller must copy
	// it.
	Value() []byte

	// Err returns the error that stopped the iteration. It returns nil if the
	// iterator reached the end.
	Err() error

	// Close releases the resources of the iterator. The caller must call it when the
	// iterator is no longer necessary. It is safe to call Close more than one time.
	// Each call after the first returns nil.
	Close() error
}
