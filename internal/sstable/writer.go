// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

var errFinished = errors.New("sstable: writer already finished")

// syncWriter is a destination that can flush its data to stable storage.
type syncWriter interface {
	io.Writer
	Sync() error
}

// Options sets the parameters of a table. A field that has the zero value gets
// its default value.
type Options struct {
	// BlockSize is the target size of a data block before compression. The default is
	// 4096.
	BlockSize int
	// RestartInterval is the number of keys between two restart points. The default
	// is 16.
	RestartInterval int
	// BitsPerKey is the number of filter bits for each key. The default is 10.
	BitsPerKey int
	// Compare orders the keys. The default is bytes.Compare.
	Compare func(a, b []byte) int
	// FilterKey returns the part of a key that goes into the bloom filter. The
	// default is the whole key.
	FilterKey func(key []byte) []byte
}

// withDefaults returns a copy of o. Each field that has the zero value gets its
// default value.
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

// Writer writes a table to dst. The caller must add keys in strictly increasing
// order.
// Finish writes the filter, the metaindex, the index and the footer, and then
// syncs dst. The Writer does not close dst.
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

// NewWriter returns a Writer that writes a table to dst. opts sets the
// parameters of the table.
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

// Add appends a key-value pair. It panics if key is not greater than the previous
// key.
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

// Finish completes the table and syncs dst. After Finish, a call to Add or Finish
// returns an error.
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

// flushData writes the current data block and adds an entry for it to the index.
// It does nothing if the block is empty.
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

// writeBlock writes block and its trailer to dst, and returns the handle of the
// block. It does not compress the block.
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

// write writes p to dst, adds the bytes to the offset and stores the first
// error.
func (w *Writer) write(p []byte) error {
	n, err := w.dst.Write(p)
	w.offset += int64(n)
	if err != nil {
		w.err = err
	}
	return err
}
