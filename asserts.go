package sediment

import "github.com/thalesdev/sediment/internal/skiplist"


var _ Iterator = (*skiplist.LockedIterator)(nil)
