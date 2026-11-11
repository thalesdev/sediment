package wal

import "io"

type syncWriter interface {
	io.Writer
	Sync() error
}

type Writer struct {
	dst         syncWriter
	blockOffset uint64
	buf         [headerSize]byte
	err         error
}

func NewWriter(dst syncWriter) *Writer {
	return &Writer{
		dst: dst,
	}
}

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

func (w *Writer) Sync() error {
	if w.err != nil {
		return w.err
	}
	err := w.dst.Sync()
	w.err = err
	return err
}

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
