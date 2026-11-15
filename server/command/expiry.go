// SPDX-License-Identifier: MIT

package command

import (
	"encoding/binary"
	"errors"
	"math"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
)

const (
	tagPlain    byte = 0
	tagDeadline byte = 1
	deadlineLen      = 8
)

var errBadHeader = errors.New("command: stored value has a bad header")

// encodeValue returns the stored form of a value. A deadline of zero means that
// the value never expires. Otherwise the deadline is a Unix time in milliseconds.
func encodeValue(deadline int64, value []byte) []byte {
	if deadline == 0 {
		out := make([]byte, 1+len(value))
		out[0] = tagPlain
		copy(out[1:], value)
		return out
	}
	out := make([]byte, 1+deadlineLen+len(value))
	out[0] = tagDeadline
	binary.LittleEndian.PutUint64(out[1:], uint64(deadline))
	copy(out[1+deadlineLen:], value)
	return out
}

// decodeValue splits a stored value into the value and its deadline. It returns
// an error, and never panics, when the header is not valid.
func decodeValue(raw []byte) ([]byte, int64, error) {
	if len(raw) == 0 {
		return nil, 0, errBadHeader
	}
	switch raw[0] {
	case tagPlain:
		return raw[1:], 0, nil
	case tagDeadline:
		if len(raw) < 1+deadlineLen {
			return nil, 0, errBadHeader
		}
		deadline := int64(binary.LittleEndian.Uint64(raw[1:]))
		if deadline <= 0 {
			return nil, 0, errBadHeader
		}
		return raw[1+deadlineLen:], deadline, nil
	default:
		return nil, 0, errBadHeader
	}
}

// ExpiredFilter returns a function for the compaction filter of the engine. It
// reports true for a stored value whose deadline has passed. It reports false
// for a value that it cannot read, so that the engine never drops what it does
// not understand.
func ExpiredFilter(now func() time.Time) func(key, value []byte) bool {
	if now == nil {
		now = time.Now
	}
	return func(_, value []byte) bool {
		_, deadline, err := decodeValue(value)
		return err == nil && deadline != 0 && deadline <= now().UnixMilli()
	}
}

// nowMillis returns the time of the server in milliseconds since the Unix epoch.
func (e *Env) nowMillis() int64 {
	if e.Now != nil {
		return e.Now().UnixMilli()
	}
	return time.Now().UnixMilli()
}

// load returns the value of key and its deadline, which is zero for a key that
// does not expire. A key whose deadline has passed is reported as not found.
func (e *Env) load(key []byte) ([]byte, int64, error) {
	raw, err := e.Store.Get(key)
	if err != nil {
		return nil, 0, err
	}
	value, deadline, err := decodeValue(raw)
	if err != nil {
		return nil, 0, err
	}
	if deadline != 0 && deadline <= e.nowMillis() {
		return nil, 0, shale.ErrNotFound
	}
	return value, deadline, nil
}

// save writes the value of key with a deadline. Zero means no expiry.
func (e *Env) save(key, value []byte, deadline int64) error {
	return e.Store.Put(key, encodeValue(deadline, value))
}

// batchPut adds a write of the value of key, with a deadline, to a batch.
func batchPut(b *shale.Batch, key, value []byte, deadline int64) {
	b.Put(key, encodeValue(deadline, value))
}

// deadlineAt returns the deadline for n units after base, in milliseconds, and
// false if it does not fit in an int64. The unit is the number of milliseconds
// in one unit of n, and n must be positive.
func deadlineAt(base, n, unit int64) (int64, bool) {
	if n > (math.MaxInt64-base)/unit {
		return 0, false
	}
	return base + n*unit, true
}

// isLive reports whether a stored value is alive at the time now, which is in
// milliseconds. It returns an error if the header of the value is not valid.
func isLive(raw []byte, now int64) (bool, error) {
	_, deadline, err := decodeValue(raw)
	if err != nil {
		return false, err
	}
	return deadline == 0 || deadline > now, nil
}
