package sstable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

var errFinished = errors.New("sstable: writer already finished")

type syncWriter interface {
	io.Writer
	Sync() error
}

type Options struct {
	// BlockSize is the target uncompressed size of a data block. Default 4096.
	BlockSize int
	// RestartInterval is the number of keys between restart points. Default 16.
	RestartInterval int
	// BitsPerKey sizes the bloom filters. Default 10.
	BitsPerKey int
	// Compare orders the keys. Default bytes.Compare.
	Compare func(a, b []byte) int
	// FilterKey extracts the part of a key that goes into the bloom filter.
	// Default: the whole key.
	FilterKey func(key []byte) []byte
}

func (o Options) withDefaults() Options {
	if o.BlockSize <= 0 {
		o.BlockSize = 4096
	}
	if o.RestartInterval <= 0 {
		o.RestartInterval = 16
	}
	if o.BitsPerKey <= 0 {
		o.BitsPerKey = 10
	}
	if o.Compare == nil {
		o.Compare = bytes.Compare
	}
	if o.FilterKey == nil {
		o.FilterKey = func(key []byte) []byte { return key }
	}
	return o
}

// Writer writes a table to dst. Keys must be added in strictly increasing order.
// Finish writes the filter, metaindex, index and footer, then syncs dst.
// The Writer never closes dst.
type Writer struct {
	dst    syncWriter
	offset int64

	data   *blockWriter
	index  *blockWriter
	filter *filterWriter

	lastKey   []byte
	entries   int
	handleBuf []byte
	trailer   [blockTrailerSize]byte

	err  error
	opts Options
}

func NewWriter(dst syncWriter, opts Options) *Writer {
	opts = opts.withDefaults()
	w := &Writer{
		dst:    dst,
		data:   newBlockWriter(opts.RestartInterval),
		index:  newBlockWriter(1),
		filter: newFilterWriter(opts.BitsPerKey),
		opts:   opts,
	}
	w.filter.startBlock(0)
	return w
}

// Add appends a key-value pair. It panics if key is not greater than the previous key.
func (w *Writer) Add(key, value []byte) error {
	if w.err != nil {
		return w.err
	}
	if w.entries > 0 && w.opts.Compare(w.lastKey, key) >= 0 {
		panic("sstable: keys must be added in strictly increasing order")
	}

	w.filter.addKey(w.opts.FilterKey(key))
	w.data.add(key, value)
	w.lastKey = append(w.lastKey[:0], key...)
	w.entries++

	if w.data.estimatedSize() >= w.opts.BlockSize {
		return w.flushData()
	}
	return nil
}

// Finish completes the table and syncs dst. Further calls to Add or Finish return an error.
func (w *Writer) Finish() error {
	if w.err != nil {
		return w.err
	}
	if err := w.flushData(); err != nil {
		return err
	}

	filterHandle, err := w.writeBlock(w.filter.finish())
	if err != nil {
		return err
	}

	metaindex := newBlockWriter(w.opts.RestartInterval)
	metaindex.add([]byte(filterMetaKey), filterHandle.append(w.handleBuf[:0]))
	metaindexHandle, err := w.writeBlock(metaindex.finish())
	if err != nil {
		return err
	}

	indexHandle, err := w.writeBlock(w.index.finish())
	if err != nil {
		return err
	}

	if err := w.write(appendFooter(nil, metaindexHandle, indexHandle)); err != nil {
		return err
	}
	if err := w.dst.Sync(); err != nil {
		w.err = err
		return err
	}

	w.err = errFinished
	return nil
}

func (w *Writer) flushData() error {
	if w.data.empty() {
		return nil
	}
	handle, err := w.writeBlock(w.data.finish())
	if err != nil {
		return err
	}
	w.handleBuf = handle.append(w.handleBuf[:0])
	w.index.add(w.lastKey, w.handleBuf)
	w.data.reset()
	w.filter.startBlock(w.offset)
	return nil
}

func (w *Writer) writeBlock(block []byte) (blockHandle, error) {
	handle := blockHandle{offset: uint64(w.offset), size: uint64(len(block))}
	w.trailer[0] = blockTypeNoCompression
	binary.LittleEndian.PutUint32(w.trailer[1:], blockChecksum(block, blockTypeNoCompression))

	if err := w.write(block); err != nil {
		return blockHandle{}, err
	}
	if err := w.write(w.trailer[:]); err != nil {
		return blockHandle{}, err
	}
	return handle, nil
}

func (w *Writer) write(p []byte) error {
	n, err := w.dst.Write(p)
	w.offset += int64(n)
	if err != nil {
		w.err = err
	}
	return err
}
