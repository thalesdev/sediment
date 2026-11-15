// SPDX-License-Identifier: MIT

package shale

import (
	"github.com/thalesdev/sediment/internal/merge"
	"github.com/thalesdev/sediment/internal/skiplist"
	"github.com/thalesdev/sediment/internal/sstable"
)

var _ Iterator = (*skiplist.Iterator)(nil)

var _ Iterator = (*sstable.Iterator)(nil)

var _ Iterator = (*merge.Iterator)(nil)

var _ Iterator = (*userIter)(nil)
