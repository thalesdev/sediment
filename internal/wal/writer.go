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

func (w *Writer) AddRecord(data []byte) error { return nil }
func (w *Writer) Sync() error                 { return nil }

func (w *Writer) writeFragment(data []byte, recordType recordType) error {
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
	w.blockOffset += uint64(headerSize + len(data))
	return nil
}
