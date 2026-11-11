package sediment

import (
	"github.com/thalesdev/sediment/internal/skiplist"
	"github.com/thalesdev/sediment/internal/sstable"
)

var _ Iterator = (*skiplist.LockedIterator)(nil)

var _ Iterator = (*sstable.Iterator)(nil)
