// SPDX-License-Identifier: MIT

package wal

import "io"

// syncWriter is a destination that can flush its data to stable storage.
type syncWriter interface {
	io.Writer
	Sync() error
}

// Writer appends records to a log. It splits a record into fragments, so that no
// fragment crosses the end of a block of blockSize bytes. After the first error,
// each method returns that error.
type Writer struct {
	dst         syncWriter
	blockOffset uint64
	buf         [headerSize]byte
	err         error
}

// NewWriter returns a Writer that writes to dst. dst must be at the start of a
// block.
func NewWriter(dst syncWriter) *Writer {
	return &Writer{
		dst: dst,
	}
}

// AddRecord appends data as one record. It does not sync dst. Call Sync to do
// this.
func (w *Writer) AddRecord(data []byte) error {
	if w.err != nil {
		return w.err
	}
	fragments := 0
	for {
		leftover := blockSize - w.blockOffset
		if leftover < headerSize {
			if _, err := w.dst.Write(make([]byte, leftover)); err != nil {
				w.err = err
				return w.err
			}
			w.blockOffset = 0
			continue
		}
		leftdata := int(leftover) - headerSize
		chunkSize := min(leftdata, len(data))
		isFirst := fragments == 0
		isLast := chunkSize == len(data)
		var recordType recordType

		if isFirst && isLast {
			recordType = recordTypeFull
		} else if isFirst {
			recordType = recordTypeFirst
		} else if isLast {
			recordType = recordTypeLast
		} else {
			recordType = recordTypeMiddle
		}

		if err := w.writeFragment(data[:chunkSize], recordType); err != nil {
			w.err = err
			return err
		}
		data = data[chunkSize:]

		fragments++

		if recordType == recordTypeLast || recordType == recordTypeFull {
			break
		}
	}
	return nil
}

// Sync flushes the data to stable storage.
func (w *Writer) Sync() error {
	if w.err != nil {
		return w.err
	}
	err := w.dst.Sync()
	w.err = err
	return err
}

// writeFragment writes the header and the payload of one fragment. The fragment
// must fit in the rest of the current block.
func (w *Writer) writeFragment(data []byte, recordType recordType) error {
	if w.err != nil {
		return w.err
	}
	if int(blockSize-w.blockOffset)-headerSize < len(data) {
		panic("wal: fragment size is more than free block size")
	}
	header := newHeader(data, recordType)
	header.marshal(w.buf[:])
	if _, err := w.dst.Write(w.buf[:]); err != nil {
		w.err = err
		return err
	}

	if _, err := w.dst.Write(data); err != nil {
		w.err = err
		return err
	}

	w.blockOffset += headerSize + uint64(len(data))
	return nil
}
