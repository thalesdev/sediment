// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
)

type boundedReaderAt struct {
	b []byte
	t testing.TB
}

func (r *boundedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if len(p) > len(r.b) {
		r.t.Errorf("read of %d bytes from a table of %d bytes", len(p), len(r.b))
		return 0, io.ErrUnexpectedEOF
	}
	if off < 0 || off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func entryBytes(shared uint64, delta, value string) []byte {
	b := binary.AppendUvarint(nil, shared)
	b = binary.AppendUvarint(b, uint64(len(delta)))
	b = binary.AppendUvarint(b, uint64(len(value)))
	return append(append(b, delta...), value...)
}

func rawBlock(entries []byte, restarts ...uint32) []byte {
	b := bytes.Clone(entries)
	for _, r := range restarts {
		b = binary.LittleEndian.AppendUint32(b, r)
	}
	return binary.LittleEndian.AppendUint32(b, uint32(len(restarts)))
}

func appendRawBlock(dst, block []byte, blockType byte, checksum uint32) ([]byte, blockHandle) {
	h := blockHandle{offset: uint64(len(dst)), size: uint64(len(block))}
	dst = append(dst, block...)
	dst = append(dst, blockType)
	return binary.LittleEndian.AppendUint32(dst, checksum), h
}

func appendGoodBlock(dst, block []byte) ([]byte, blockHandle) {
	return appendRawBlock(dst, block, blockTypeNoCompression, blockChecksum(block, blockTypeNoCompression))
}

type craft struct {
	data       []byte
	lastKey    string
	filter     []byte
	meta       []kv
	indexValue func(h blockHandle) []byte
}

func (c craft) build() []byte {
	if c.data == nil {
		c.data = buildBlock([]kv{{[]byte("a"), []byte("1")}, {[]byte("b"), []byte("2")}}, 16)
		c.lastKey = "b"
	}
	if c.indexValue == nil {
		c.indexValue = func(h blockHandle) []byte { return h.append(nil) }
	}
	b, dataHandle := appendGoodBlock(nil, c.data)
	meta := newBlockWriter(16)
	entries := c.meta
	if c.filter != nil {
		var filterHandle blockHandle
		b, filterHandle = appendGoodBlock(b, c.filter)
		entries = append(entries, kv{[]byte(filterMetaKey), filterHandle.append(nil)})
	}
	for _, e := range sortedKV(entries) {
		meta.add(e.key, e.value)
	}
	b, metaHandle := appendGoodBlock(b, meta.finish())
	index := newBlockWriter(1)
	index.add([]byte(c.lastKey), c.indexValue(dataHandle))
	b, indexHandle := appendGoodBlock(b, index.finish())
	return appendFooter(b, metaHandle, indexHandle)
}

func sortedKV(in []kv) []kv {
	out := append([]kv(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && bytes.Compare(out[j-1].key, out[j].key) > 0; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func filterBlock(body []byte, offsets []uint32, base byte) []byte {
	fb := bytes.Clone(body)
	arrayOffset := uint32(len(fb))
	for _, o := range offsets {
		fb = binary.LittleEndian.AppendUint32(fb, o)
	}
	fb = binary.LittleEndian.AppendUint32(fb, arrayOffset)
	return append(fb, base)
}

func openBounded(t testing.TB, data []byte) (*Reader, error) {
	t.Helper()
	return Open(&boundedReaderAt{b: data, t: t}, int64(len(data)), Options{})
}

func mustOpen(t *testing.T, data []byte) *Reader {
	t.Helper()
	r, err := openBounded(t, data)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

func assertCorrupt(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errCorrupt) {
		t.Fatalf("got %v, want an error that wraps errCorrupt", err)
	}
}

func checkAgainst(t *testing.T, data []byte, want []kv, what string) {
	t.Helper()
	r, err := openBounded(t, data)
	if err != nil {
		return
	}
	it := r.NewIterator()
	var got []kv
	for it.SeekToFirst(); it.Valid(); it.Next() {
		got = append(got, kv{bytes.Clone(it.Key()), bytes.Clone(it.Value())})
	}
	iterErr := it.Err()
	it.Close()
	if len(got) > len(want) {
		t.Fatalf("%s: iterator returned %d entries, want at most %d", what, len(got), len(want))
	}
	for i := range got {
		if !bytes.Equal(got[i].key, want[i].key) || !bytes.Equal(got[i].value, want[i].value) {
			t.Fatalf("%s: entry %d is %q, want %q", what, i, got[i].key, want[i].key)
		}
	}
	if iterErr == nil && len(got) != len(want) {
		t.Fatalf("%s: iterator ended without an error after %d of %d entries", what, len(got), len(want))
	}
	for _, e := range want {
		v, ok, err := r.Get(e.key)
		if err == nil && (!ok || !bytes.Equal(v, e.value)) {
			t.Fatalf("%s: Get(%q) = %q, %v with no error", what, e.key, v, ok)
		}
	}
}

func TestReaderBitFlips(t *testing.T) {
	t.Run("Should return a correct result or an error after the flip of any byte", func(t *testing.T) {
		entries := numbered(120, 100)
		good := writeTable(t, Options{BlockSize: 512}, entries)
		data := bytes.Clone(good)
		for pos := range data {
			data[pos] ^= 0xff
			checkAgainst(t, data, entries, "flip at "+strconv.Itoa(pos))
			data[pos] ^= 0xff
		}
	})

	t.Run("Should return a correct result or an error after the flip of one bit in any byte", func(t *testing.T) {
		entries := numbered(40, 60)
		good := writeTable(t, Options{BlockSize: 256}, entries)
		data := bytes.Clone(good)
		for pos := range data {
			for bit := range 8 {
				data[pos] ^= 1 << bit
				checkAgainst(t, data, entries, "flip at "+strconv.Itoa(pos)+" bit "+strconv.Itoa(bit))
				data[pos] ^= 1 << bit
			}
		}
	})

	t.Run("Should return an error for a table that is cut at any size", func(t *testing.T) {
		good := writeTable(t, Options{BlockSize: 512}, numbered(120, 100))
		for n := range len(good) {
			if _, err := openBounded(t, good[:n]); err == nil {
				t.Fatalf("Open accepted a table cut to %d of %d bytes", n, len(good))
			}
		}
	})
}

func TestReaderHandles(t *testing.T) {
	base := craft{}.build()
	good := base[len(base)-footerSize:]
	metaHandle, n := decodeBlockHandle(good)
	indexHandle, _ := decodeBlockHandle(good[n:])
	body := base[:len(base)-footerSize]
	huge := []blockHandle{
		{math.MaxUint64, 10},
		{10, math.MaxUint64},
		{1 << 63, 1 << 63},
		{0, 1 << 40},
		{uint64(len(body)), 1},
		{math.MaxUint64, math.MaxUint64},
		{0, uint64(len(base)) - 4},
	}
	for _, h := range huge {
		t.Run("Should reject the handle "+strconv.Itoa(int(h.offset%1000))+","+strconv.Itoa(int(h.size%1000)), func(t *testing.T) {
			for _, table := range [][]byte{
				appendFooter(bytes.Clone(body), h, indexHandle),
				appendFooter(bytes.Clone(body), metaHandle, h),
			} {
				_, err := openBounded(t, table)
				assertCorrupt(t, err)
			}
		})
	}

	t.Run("Should reject a footer with a varint that is cut", func(t *testing.T) {
		footer := bytes.Repeat([]byte{0xff}, footerSize-8)
		footer = binary.LittleEndian.AppendUint64(footer, tableMagic)
		_, err := openBounded(t, append(bytes.Clone(body), footer...))
		assertCorrupt(t, err)
	})

	t.Run("Should reject a table that is smaller than the footer and one with a bad magic number", func(t *testing.T) {
		_, err := openBounded(t, base[:footerSize-1])
		assertCorrupt(t, err)
		bad := bytes.Clone(base)
		bad[len(bad)-1] ^= 1
		_, err = openBounded(t, bad)
		assertCorrupt(t, err)
	})

	t.Run("Should reject an index value that is longer or shorter than a handle", func(t *testing.T) {
		for name, value := range map[string]func(h blockHandle) []byte{
			"extra byte":   func(h blockHandle) []byte { return append(h.append(nil), 0) },
			"missing byte": func(h blockHandle) []byte { return h.append(nil)[:len(h.append(nil))-1] },
			"empty":        func(h blockHandle) []byte { return nil },
		} {
			r := mustOpen(t, craft{indexValue: value}.build())
			_, _, err := r.Get([]byte("a"))
			assertCorrupt(t, err)
			it := r.NewIterator()
			it.SeekToFirst()
			if it.Valid() {
				t.Fatalf("%s: iterator is valid", name)
			}
			assertCorrupt(t, it.Err())
		}
	})
}

func TestReaderBlocks(t *testing.T) {
	data := buildBlock([]kv{{[]byte("a"), []byte("1")}, {[]byte("b"), []byte("2")}}, 16)

	t.Run("Should reject a block with a wrong checksum", func(t *testing.T) {
		table := craft{}.build()
		table[2] ^= 1
		_, _, err := mustOpen(t, table).Get([]byte("a"))
		assertCorrupt(t, err)
	})

	t.Run("Should reject a compressed block, also when the checksum is correct", func(t *testing.T) {
		b, dataHandle := appendRawBlock(nil, data, 1, blockChecksum(data, 1))
		meta := newBlockWriter(16)
		b, metaHandle := appendGoodBlock(b, meta.finish())
		index := newBlockWriter(1)
		index.add([]byte("b"), dataHandle.append(nil))
		b, indexHandle := appendGoodBlock(b, index.finish())
		r := mustOpen(t, appendFooter(b, metaHandle, indexHandle))
		_, _, err := r.Get([]byte("a"))
		assertCorrupt(t, err)
		if !strings.Contains(err.Error(), "compression") {
			t.Fatalf("got %q, want a message about compression", err)
		}
	})

	t.Run("Should reject a block with a checksum for the wrong type", func(t *testing.T) {
		b, dataHandle := appendRawBlock(nil, data, 1, blockChecksum(data, 0))
		meta := newBlockWriter(16)
		b, metaHandle := appendGoodBlock(b, meta.finish())
		index := newBlockWriter(1)
		index.add([]byte("b"), dataHandle.append(nil))
		b, indexHandle := appendGoodBlock(b, index.finish())
		_, _, err := mustOpen(t, appendFooter(b, metaHandle, indexHandle)).Get([]byte("a"))
		assertCorrupt(t, err)
	})
}

func TestBlockIterCorruption(t *testing.T) {
	cases := map[string][]byte{
		"shared prefix longer than the last key": rawBlock(append(entryBytes(0, "a", "v"), entryBytes(5, "b", "v")...), 0),
		"truncated varint":                       rawBlock([]byte{0x80}, 0),
		"entry that overflows the block":         rawBlock([]byte{0, 9, 9, 'a'}, 0),
		"unshared length near 2^64":              rawBlock(append(append([]byte{0}, binary.AppendUvarint(nil, math.MaxUint64)...), 1), 0),
		"sum of lengths that overflows":          rawBlock(append(append([]byte{0}, binary.AppendUvarint(nil, 1<<63)...), binary.AppendUvarint(nil, 1<<63)...), 0),
		"shared length near 2^64":                rawBlock(binary.AppendUvarint(nil, math.MaxUint64), 0),
	}
	for name, block := range cases {
		t.Run("Should return an error for a "+name, func(t *testing.T) {
			it, err := newBlockIter(block, bytes.Compare)
			if err != nil {
				assertCorrupt(t, err)
				return
			}
			it.SeekToFirst()
			for it.Valid() {
				it.Next()
			}
			assertCorrupt(t, it.Err())
			it.Seek([]byte("a"))
			if it.Valid() {
				t.Fatal("Seek after an error must not be valid")
			}
		})
	}

	t.Run("Should reject a bad restart array", func(t *testing.T) {
		two := append(entryBytes(0, "a", "v"), entryBytes(0, "b", "v")...)
		second := uint32(len(entryBytes(0, "a", "v")))
		bad := map[string][]byte{
			"count of zero":                 {0, 0, 0, 0},
			"count larger than the block":   binary.LittleEndian.AppendUint32(append([]byte{}, two...), math.MaxUint32),
			"count that overflows 32 bits":  binary.LittleEndian.AppendUint32(nil, 1<<30),
			"restart past the entries":      rawBlock(two, 0, uint32(len(two))),
			"restart that is not ascending": rawBlock(two, 0, second, second),
			"restarts in descending order":  rawBlock(two, second, 0),
			"first restart that is not 0":   rawBlock(two, second),
			"restart at 2^32-1":             rawBlock(two, 0, math.MaxUint32),
		}
		for name, block := range bad {
			if _, err := newBlockIter(block, bytes.Compare); !errors.Is(err, errCorrupt) {
				t.Fatalf("%s: got %v, want errCorrupt", name, err)
			}
		}
	})

	t.Run("Should return an error for a restart entry that shares a prefix", func(t *testing.T) {
		first := entryBytes(0, "ab", "v")
		block := rawBlock(append(first, entryBytes(1, "c", "v")...), 0, uint32(len(first)))
		it, err := newBlockIter(block, bytes.Compare)
		if err != nil {
			t.Fatal(err)
		}
		it.SeekToFirst()
		it.Next()
		assertCorrupt(t, it.Err())

		it, _ = newBlockIter(block, bytes.Compare)
		it.Seek([]byte("zzz"))
		if it.Valid() {
			t.Fatal("Seek must not be valid")
		}
		assertCorrupt(t, it.Err())
	})

	t.Run("Should return an error for a restart that points inside an entry", func(t *testing.T) {
		first := entryBytes(0, "abc", "value")
		block := rawBlock(append(first, entryBytes(0, "d", "v")...), 0, 3)
		it, err := newBlockIter(block, bytes.Compare)
		if err != nil {
			t.Fatal(err)
		}
		it.SeekToFirst()
		it.Next()
		assertCorrupt(t, it.Err())
	})
}

func TestReaderFilter(t *testing.T) {
	malformed := map[string][]byte{
		"smaller than 5 bytes":           {1, 2, 3, 4},
		"array that starts past the end": append(binary.LittleEndian.AppendUint32(make([]byte, 3), 200), filterBaseLg),
		"offsets out of order":           filterBlock(make([]byte, 16), []uint32{8, 4}, filterBaseLg),
		"offset past the array":          filterBlock(make([]byte, 16), []uint32{0, 99}, filterBaseLg),
		"array that is not a multiple":   append(append(make([]byte, 19), binary.LittleEndian.AppendUint32(nil, 16)...), filterBaseLg),
	}
	for name, fb := range malformed {
		t.Run("Should reject a filter that is "+name, func(t *testing.T) {
			_, err := openBounded(t, craft{filter: fb}.build())
			assertCorrupt(t, err)
		})
	}

	t.Run("Should read the table when the filter does not cover the block", func(t *testing.T) {
		r := mustOpen(t, craft{filter: filterBlock(nil, nil, filterBaseLg)}.build())
		if v, ok, err := r.Get([]byte("a")); err != nil || !ok || string(v) != "1" {
			t.Fatalf("Get = %q, %v, %v", v, ok, err)
		}
	})

	t.Run("Should ignore a filter with an unknown base and read the data", func(t *testing.T) {
		r := mustOpen(t, craft{filter: filterBlock(nil, []uint32{0}, filterBaseLg+1)}.build())
		if v, ok, err := r.Get([]byte("b")); err != nil || !ok || string(v) != "2" {
			t.Fatalf("Get = %q, %v, %v", v, ok, err)
		}
	})

	t.Run("Should work without a filter and ignore unknown metaindex keys", func(t *testing.T) {
		for name, meta := range map[string][]kv{
			"no entries":       nil,
			"unknown before":   {{[]byte("a.unknown"), []byte("x")}},
			"unknown after":    {{[]byte("z.unknown"), []byte("x")}},
			"unknown on sides": {{[]byte("a.unknown"), []byte("x")}, {[]byte("z.unknown"), []byte("x")}},
		} {
			r := mustOpen(t, craft{meta: meta}.build())
			if v, ok, err := r.Get([]byte("a")); err != nil || !ok || string(v) != "1" {
				t.Fatalf("%s: Get = %q, %v, %v", name, v, ok, err)
			}
			withFilter := mustOpen(t, craft{meta: meta, filter: filterBlock(nil, nil, filterBaseLg)}.build())
			if v, ok, err := withFilter.Get([]byte("a")); err != nil || !ok || string(v) != "1" {
				t.Fatalf("%s with filter: Get = %q, %v, %v", name, v, ok, err)
			}
		}
	})
}

func TestReaderErrorsAreSticky(t *testing.T) {
	entries := numbered(120, 100)
	good := writeTable(t, Options{BlockSize: 512}, entries)
	parsed := parseTable(t, good)
	if len(parsed.dataBlocks) < 3 {
		t.Fatalf("table has %d data blocks, want at least 3", len(parsed.dataBlocks))
	}
	data := bytes.Clone(good)
	data[parsed.dataBlocks[1].offset+3] ^= 0xff
	r := mustOpen(t, data)

	t.Run("Should stop the iterator at the bad block and keep the error", func(t *testing.T) {
		it := r.NewIterator()
		count := 0
		for it.SeekToFirst(); it.Valid(); it.Next() {
			count++
		}
		assertCorrupt(t, it.Err())
		if count == 0 || count >= len(entries) {
			t.Fatalf("iterator returned %d entries before the error", count)
		}
		it.SeekToFirst()
		if it.Valid() {
			t.Fatal("SeekToFirst after an error must not be valid")
		}
		it.Seek(entries[0].key)
		if it.Valid() {
			t.Fatal("Seek after an error must not be valid")
		}
		assertCorrupt(t, it.Err())
	})

	t.Run("Should return an error from Get for a key in the bad block", func(t *testing.T) {
		var inBad kv
		for _, e := range entries {
			if bytes.Compare(e.key, parsed.indexKeys[0]) > 0 && bytes.Compare(e.key, parsed.indexKeys[1]) <= 0 {
				inBad = e
				break
			}
		}
		_, ok, err := r.Get(inBad.key)
		assertCorrupt(t, err)
		if ok {
			t.Fatal("Get reported a key in a bad block")
		}
		if v, ok, err := r.Get(entries[0].key); err != nil || !ok || !bytes.Equal(v, entries[0].value) {
			t.Fatalf("Get in a good block = %v, %v", ok, err)
		}
	})

	t.Run("Should return an error from Seek on a bad block", func(t *testing.T) {
		it := r.NewIterator()
		it.Seek(parsed.indexKeys[1])
		if it.Valid() {
			t.Fatal("Seek into a bad block must not be valid")
		}
		assertCorrupt(t, it.Err())
	})
}

func tableBytes(opts Options, entries []kv) []byte {
	dst := &fakeDst{}
	w := NewWriter(dst, opts)
	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			panic(err)
		}
	}
	if err := w.Finish(); err != nil {
		panic(err)
	}
	return dst.buf.Bytes()
}

func FuzzReader(f *testing.F) {
	f.Add([]byte{})
	f.Add(tableBytes(Options{}, nil))
	f.Add(tableBytes(Options{}, numbered(1, 3)))
	f.Add(tableBytes(Options{BlockSize: 256}, numbered(60, 20)))
	f.Add(craft{filter: filterBlock(nil, []uint32{0}, filterBaseLg)}.build())
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := openBounded(t, data)
		if err != nil {
			return
		}
		it := r.NewIterator()
		steps := 0
		for it.SeekToFirst(); it.Valid() && steps < 1<<16; it.Next() {
			_, _ = it.Key(), it.Value()
			steps++
		}
		for it.Seek([]byte("key-00000010")); it.Valid() && steps < 1<<17; it.Next() {
			_, _ = it.Key(), it.Value()
			steps++
		}
		it.Close()
		_, _, _ = r.Get([]byte("key-00000010"))
		_, _, _ = r.Get(nil)
	})
}

func TestReaderEmptyBlocks(t *testing.T) {
	t.Run("Should open and iterate a table that our writer wrote with no keys", func(t *testing.T) {
		r := mustOpen(t, tableBytes(Options{}, nil))
		it := r.NewIterator()
		it.SeekToFirst()
		if it.Valid() || it.Err() != nil {
			t.Fatalf("SeekToFirst: valid=%v err=%v", it.Valid(), it.Err())
		}
		it.Seek([]byte("a"))
		if it.Valid() || it.Err() != nil {
			t.Fatalf("Seek: valid=%v err=%v", it.Valid(), it.Err())
		}
		if _, ok, err := r.Get([]byte("a")); ok || err != nil {
			t.Fatalf("Get = %v, %v", ok, err)
		}
	})

	t.Run("Should accept an empty data block that the index points at", func(t *testing.T) {
		r := mustOpen(t, craft{data: buildBlock(nil, 16), lastKey: "b"}.build())
		it := r.NewIterator()
		it.SeekToFirst()
		if it.Valid() || it.Err() != nil {
			t.Fatalf("SeekToFirst: valid=%v err=%v", it.Valid(), it.Err())
		}
		it.Seek([]byte("a"))
		if it.Valid() || it.Err() != nil {
			t.Fatalf("Seek: valid=%v err=%v", it.Valid(), it.Err())
		}
		if _, ok, err := r.Get([]byte("a")); ok || err != nil {
			t.Fatalf("Get = %v, %v", ok, err)
		}
	})

	t.Run("Should accept the restart 0 in a block with no entries and reject any other", func(t *testing.T) {
		if _, err := newBlockIter(rawBlock(nil, 0), bytes.Compare); err != nil {
			t.Fatalf("empty block with restart 0: %v", err)
		}
		for _, block := range [][]byte{rawBlock(nil, 1), rawBlock(nil, 0, 0)} {
			if _, err := newBlockIter(block, bytes.Compare); !errors.Is(err, errCorrupt) {
				t.Fatalf("got %v, want errCorrupt", err)
			}
		}
	})
}
