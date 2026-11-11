// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/thalesdev/sediment/internal/bloom"
)

type entry struct {
	key, value []byte
	shared     int
	offset     int
}

type kv struct{ key, value []byte }

func decodeBlock(t *testing.T, b []byte) ([]entry, []uint32) {
	t.Helper()
	if len(b) < 4 {
		t.Fatalf("block of %d bytes has no restart count", len(b))
	}
	n := int(binary.LittleEndian.Uint32(b[len(b)-4:]))
	start := len(b) - 4 - 4*n
	if n == 0 || start < 0 {
		t.Fatalf("invalid restart count %d for block of %d bytes", n, len(b))
	}
	restarts := make([]uint32, n)
	for i := range restarts {
		restarts[i] = binary.LittleEndian.Uint32(b[start+4*i:])
	}

	data := b[:start]
	var entries []entry
	var key []byte
	for pos := 0; pos < len(data); {
		offset := pos
		read := func() int {
			v, k := binary.Uvarint(data[pos:])
			if k <= 0 {
				t.Fatalf("bad varint at offset %d", pos)
			}
			pos += k
			return int(v)
		}
		shared, unshared, valueLen := read(), read(), read()
		if shared > len(key) || pos+unshared+valueLen > len(data) {
			t.Fatalf("entry at offset %d out of range", offset)
		}
		key = append(key[:shared], data[pos:pos+unshared]...)
		pos += unshared
		value := data[pos : pos+valueLen]
		pos += valueLen
		entries = append(entries, entry{bytes.Clone(key), bytes.Clone(value), shared, offset})
	}
	return entries, restarts
}

type parsedTable struct {
	entries    []kv
	dataBlocks []blockHandle
	indexKeys  [][]byte
	filter     []byte
}

func readBlock(t *testing.T, b []byte, h blockHandle) []byte {
	t.Helper()
	end := int(h.offset + h.size)
	if end+blockTrailerSize > len(b) {
		t.Fatalf("block %+v out of range", h)
	}
	block := b[h.offset:end]
	if b[end] != blockTypeNoCompression {
		t.Fatalf("block %+v has type %d", h, b[end])
	}
	if got, want := binary.LittleEndian.Uint32(b[end+1:]), blockChecksum(block, b[end]); got != want {
		t.Fatalf("block %+v checksum %#08x, want %#08x", h, got, want)
	}
	return block
}

func parseTable(t *testing.T, b []byte) parsedTable {
	t.Helper()
	if len(b) < footerSize {
		t.Fatalf("table of %d bytes is smaller than the footer", len(b))
	}
	footer := b[len(b)-footerSize:]
	if binary.LittleEndian.Uint64(footer[footerSize-8:]) != tableMagic {
		t.Fatal("bad magic number")
	}
	metaHandle, n := decodeBlockHandle(footer)
	indexHandle, m := decodeBlockHandle(footer[max(n, 0):])
	if n <= 0 || m <= 0 {
		t.Fatal("malformed footer handles")
	}
	for _, c := range footer[n+m : footerSize-8] {
		if c != 0 {
			t.Fatal("footer padding is not zero")
		}
	}

	var p parsedTable
	meta, _ := decodeBlock(t, readBlock(t, b, metaHandle))
	if len(meta) != 1 || string(meta[0].key) != filterMetaKey {
		t.Fatalf("metaindex has %d entries, want only %q", len(meta), filterMetaKey)
	}
	filterHandle, k := decodeBlockHandle(meta[0].value)
	if k != len(meta[0].value) {
		t.Fatal("malformed filter handle")
	}
	p.filter = readBlock(t, b, filterHandle)

	index, restarts := decodeBlock(t, readBlock(t, b, indexHandle))
	if len(index) > 0 && len(restarts) != len(index) {
		t.Fatalf("index has %d restarts for %d entries, want one per entry", len(restarts), len(index))
	}
	var expectedOffset uint64
	for _, ie := range index {
		h, k := decodeBlockHandle(ie.value)
		if k != len(ie.value) {
			t.Fatal("malformed index handle")
		}
		if h.offset != expectedOffset {
			t.Fatalf("data block at %d, want %d", h.offset, expectedOffset)
		}
		expectedOffset = h.offset + h.size + blockTrailerSize
		entries, _ := decodeBlock(t, readBlock(t, b, h))
		if len(entries) == 0 {
			t.Fatalf("data block %+v is empty", h)
		}
		if !bytes.Equal(entries[len(entries)-1].key, ie.key) {
			t.Fatalf("index key %q, want last key of block %q", ie.key, entries[len(entries)-1].key)
		}
		for _, e := range entries {
			p.entries = append(p.entries, kv{e.key, e.value})
		}
		p.dataBlocks = append(p.dataBlocks, h)
		p.indexKeys = append(p.indexKeys, ie.key)
	}
	if expectedOffset != filterHandle.offset {
		t.Fatalf("filter block at %d, want right after the data blocks at %d", filterHandle.offset, expectedOffset)
	}
	return p
}

func filterMayContain(t *testing.T, fb []byte, blockOffset uint64, key []byte) bool {
	t.Helper()
	if len(fb) < 5 {
		t.Fatal("filter block too small")
	}
	lg := fb[len(fb)-1]
	arrayOffset := int(binary.LittleEndian.Uint32(fb[len(fb)-5:]))
	num := (len(fb) - 5 - arrayOffset) / 4
	idx := int(blockOffset >> lg)
	if idx >= num {
		t.Fatalf("no filter for block at %d (%d filters)", blockOffset, num)
	}
	start := int(binary.LittleEndian.Uint32(fb[arrayOffset+4*idx:]))
	limit := arrayOffset
	if idx+1 < num {
		limit = int(binary.LittleEndian.Uint32(fb[arrayOffset+4*(idx+1):]))
	}
	return bloom.Filter(fb[start:limit]).MayContain(key)
}

func writeTable(t *testing.T, opts Options, entries []kv) []byte {
	t.Helper()
	dst := &fakeDst{}
	w := NewWriter(dst, opts)
	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return dst.buf.Bytes()
}

func numbered(n int, valueSize int) []kv {
	out := make([]kv, n)
	for i := range out {
		out[i] = kv{fmt.Appendf(nil, "key-%08d", i), bytes.Repeat([]byte{byte(i)}, valueSize)}
	}
	return out
}

func assertEntries(t *testing.T, got, want []kv) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i].key, want[i].key) || !bytes.Equal(got[i].value, want[i].value) {
			t.Fatalf("entry %d: got key %q, want %q", i, got[i].key, want[i].key)
		}
	}
}
