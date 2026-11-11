package wal

import (
	"errors"
	"io"
)

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

func NewReader(r io.Reader) *Reader {
	return &Reader{
		src: r,
	}
}

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

func (r *Reader) Dropped() int {
	return r.dropped
}

func (r *Reader) outOfSequence(payload []byte) ([]byte, error) {
	r.dropped += len(r.record) + headerSize + len(payload)
	r.record = r.record[:0]
	r.corrupt()
	return nil, io.EOF
}

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

func (r *Reader) corrupt() (recordType, []byte, error) {
	r.dropped += r.blockLen - r.blockOffset
	r.blockOffset = r.blockLen
	r.eof = true
	return recordTypeZero, nil, io.EOF
}
