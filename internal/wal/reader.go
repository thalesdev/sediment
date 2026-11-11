// SPDX-License-Identifier: MIT

package wal

import (
	"errors"
	"io"
)

// Reader reads the records that a Writer wrote. It checks the checksum of each
// fragment. At the first data that is not valid, it ends the log: Next returns
// io.EOF and Dropped counts the bytes that it skipped.
type Reader struct {
	src    io.Reader
	block  [blockSize]byte
	record []byte

	blockOffset int
	blockLen    int
	dropped     int
	eof         bool

	err error
}

// NewReader returns a Reader that reads from r. r must be at the start of a
// block.
func NewReader(r io.Reader) *Reader {
	return &Reader{
		src: r,
	}
}

// Next returns the next record. The slice is valid only until the next call to
// Next. At the end of the log, and at the first data that is not valid, it
// returns io.EOF. Any other read error is returned again by each later call.
func (r *Reader) Next() ([]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	r.record = r.record[:0]
	inRecord := false

	for {
		typ, payload, err := r.nextFragment()
		if err != nil {
			if inRecord {
				r.dropped += len(r.record)
			}
			return nil, err
		}

		switch typ {
		case recordTypeFull:
			if inRecord {
				return r.outOfSequence(payload)
			}
			return payload, nil
		case recordTypeFirst:
			if inRecord {
				return r.outOfSequence(payload)
			}
			r.record = append(r.record, payload...)
			inRecord = true
		case recordTypeMiddle:
			if !inRecord {
				return r.outOfSequence(payload)
			}
			r.record = append(r.record, payload...)
		case recordTypeLast:
			if !inRecord {
				return r.outOfSequence(payload)
			}
			r.record = append(r.record, payload...)
			return r.record, nil
		}
	}
}

// Dropped returns the number of bytes that the Reader skipped because they were
// not valid.
func (r *Reader) Dropped() int {
	return r.dropped
}

// outOfSequence counts the incomplete record and the payload as dropped, ends
// the log and returns io.EOF. Next calls it when the type of a fragment does not
// follow the previous fragment.
func (r *Reader) outOfSequence(payload []byte) ([]byte, error) {
	r.dropped += len(r.record) + headerSize + len(payload)
	r.record = r.record[:0]
	r.corrupt()
	return nil, io.EOF
}

// nextFragment returns the type and the payload of the next valid fragment. When
// the current block has no more fragments, it reads the next block. It skips
// padding.
func (r *Reader) nextFragment() (recordType, []byte, error) {
	for {
		if r.blockLen-r.blockOffset < headerSize {
			if r.eof {
				r.dropped += r.blockLen - r.blockOffset
				r.blockOffset = r.blockLen
				return recordTypeZero, nil, io.EOF
			}
			n, err := io.ReadFull(r.src, r.block[:])
			if errors.Is(err, io.EOF) {
				r.eof = true
				r.blockOffset = r.blockLen
				return recordTypeZero, nil, io.EOF
			} else if errors.Is(err, io.ErrUnexpectedEOF) {
				r.eof = true
			} else if err != nil {
				r.err = err
				return recordTypeZero, nil, err
			}
			r.blockLen = n
			r.blockOffset = 0
			continue
		}

		var h header
		h.unmarshal(r.block[r.blockOffset:])

		if h.recordType == recordTypeZero {
			r.blockOffset = r.blockLen
			continue
		}
		if !h.recordType.Valid() {
			return r.corrupt()
		}

		start := r.blockOffset + headerSize
		end := start + int(h.length)
		if end > r.blockLen {
			return r.corrupt()
		}

		payload := r.block[start:end]
		if h.computeChecksum(payload) != h.checksum {
			return r.corrupt()
		}

		r.blockOffset = end
		return h.recordType, payload, nil
	}
}

// corrupt counts the rest of the block as dropped, ends the log and returns
// io.EOF.
func (r *Reader) corrupt() (recordType, []byte, error) {
	r.dropped += r.blockLen - r.blockOffset
	r.blockOffset = r.blockLen
	r.eof = true
	return recordTypeZero, nil, io.EOF
}
