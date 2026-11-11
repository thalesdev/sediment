package sediment

// Iterator iterates over a set of key-value pairs in ascending key order,
// according to the iterator's comparator.
//
// It generalizes iteration over memtables, on-disk tables, and merged views.
// An Iterator starts unpositioned: SeekToFirst or Seek must be called before
// any other method. Using an Iterator after Close is undefined.
type Iterator interface {
	// SeekToFirst positions the iterator at the first key-value pair.
	SeekToFirst()

	// Seek positions the iterator at the first key-value pair whose key is
	// greater than or equal to key.
	Seek(key []byte)

	// Next moves the iterator to the next key-value pair. If there is none,
	// Valid reports false afterwards.
	// Calling Next when Valid reports false is undefined.
	Next()

	// Valid reports whether the iterator is positioned at a key-value pair.
	// It returns false when the iterator has reached the end or encountered
	// an error; check Err to tell them apart.
	Valid() bool

	// Key returns the key of the current pair. It must only be called when
	// Valid reports true.
	// The returned slice is valid only until the next call to SeekToFirst,
	// Seek, Next, or Close, and must not be modified. Callers that need to
	// keep it must copy it.
	Key() []byte

	// Value returns the value of the current pair. It must only be called when
	// Valid reports true.
	// The returned slice is valid only until the next call to SeekToFirst,
	// Seek, Next, or Close, and must not be modified. Callers that need to
	// keep it must copy it.
	Value() []byte

	// Err returns the error that stopped the iteration, or nil if the
	// iterator simply reached the end.
	Err() error

	// Close releases the resources held by the iterator and must be called
	// once it is no longer needed. Calling Close more than once is safe;
	// subsequent calls return nil.
	Close() error
}
