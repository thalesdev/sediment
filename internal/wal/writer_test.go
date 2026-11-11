// SPDX-License-Identifier: MIT

package wal

import (
	"bytes"
	"errors"
	"hash/crc32"
	"math/rand/v2"
	"testing"
)

var (
	errWrite = errors.New("write failed")
	errSync  = errors.New("sync failed")
)

type fakeDst struct {
	buf         bytes.Buffer
	writes      int
	syncs       int
	failWriteAt int
	syncErr     error
}

func (f *fakeDst) Write(p []byte) (int, error) {
	f.writes++
	if f.failWriteAt > 0 && f.writes >= f.failWriteAt {
		return 0, errWrite
	}
	return f.buf.Write(p)
}

func (f *fakeDst) Sync() error {
	f.syncs++
	return f.syncErr
}

type fragment struct {
	typ     recordType
	payload []byte
	block   int
	offset  int
}

func payloadOf(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31) + seed
	}
	return b
}

func parseFragments(t *testing.T, b []byte) []fragment {
	t.Helper()
	var out []fragment
	for pos := 0; pos < len(b); {
		block, off := pos/blockSize, pos%blockSize
		left := blockSize - off
		if left < headerSize {
			end := min(pos+left, len(b))
			for i, c := range b[pos:end] {
				if c != 0 {
					t.Fatalf("trailer byte at block %d offset %d is %#x, want 0", block, off+i, c)
				}
			}
			pos += left
			continue
		}
		if len(b)-pos < headerSize {
			t.Fatalf("truncated header at block %d offset %d", block, off)
		}
		var h header
		h.unmarshal(b[pos:])
		if !h.recordType.Valid() || h.recordType == recordTypeZero {
			t.Fatalf("invalid record type %d at block %d offset %d", h.recordType, block, off)
		}
		if off+headerSize+int(h.length) > blockSize {
			t.Fatalf("fragment at block %d offset %d crosses block boundary (length %d)", block, off, h.length)
		}
		start, end := pos+headerSize, pos+headerSize+int(h.length)
		if end > len(b) {
			t.Fatalf("fragment at block %d offset %d truncated", block, off)
		}
		payload := b[start:end]
		want := maskChecksum(crc32.Checksum(append([]byte{byte(h.recordType)}, payload...), crcTable))
		if h.checksum != want {
			t.Fatalf("checksum mismatch at block %d offset %d: got %#08x, want %#08x", block, off, h.checksum, want)
		}
		out = append(out, fragment{h.recordType, payload, block, off})
		pos = end
	}
	return out
}

func assemble(t *testing.T, frags []fragment) [][]byte {
	t.Helper()
	var records [][]byte
	var cur []byte
	inRecord := false
	for i, f := range frags {
		switch f.typ {
		case recordTypeFull:
			if inRecord {
				t.Fatalf("fragment %d: FULL inside a fragmented record", i)
			}
			records = append(records, append([]byte{}, f.payload...))
		case recordTypeFirst:
			if inRecord {
				t.Fatalf("fragment %d: FIRST inside a fragmented record", i)
			}
			cur, inRecord = append([]byte{}, f.payload...), true
		case recordTypeMiddle:
			if !inRecord {
				t.Fatalf("fragment %d: MIDDLE without FIRST", i)
			}
			cur = append(cur, f.payload...)
		case recordTypeLast:
			if !inRecord {
				t.Fatalf("fragment %d: LAST without FIRST", i)
			}
			records = append(records, append(cur, f.payload...))
			cur, inRecord = nil, false
		}
	}
	if inRecord {
		t.Fatal("log ends in the middle of a fragmented record")
	}
	return records
}

func writeAll(t *testing.T, records ...[]byte) *fakeDst {
	t.Helper()
	dst := &fakeDst{}
	w := NewWriter(dst)
	for i, r := range records {
		if err := w.AddRecord(r); err != nil {
			t.Fatalf("AddRecord %d: %v", i, err)
		}
	}
	return dst
}

func types(frags []fragment) []recordType {
	out := make([]recordType, len(frags))
	for i, f := range frags {
		out[i] = f.typ
	}
	return out
}

func equalTypes(a, b []recordType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWriterLayout(t *testing.T) {
	t.Run("Should write empty record as a single FULL fragment of length zero", func(t *testing.T) {
		dst := writeAll(t, []byte{})
		if dst.buf.Len() != headerSize {
			t.Fatalf("got %d bytes, want %d", dst.buf.Len(), headerSize)
		}
		frags := parseFragments(t, dst.buf.Bytes())
		if len(frags) != 1 || frags[0].typ != recordTypeFull || len(frags[0].payload) != 0 {
			t.Fatalf("got %+v, want one empty FULL fragment", frags)
		}
	})

	t.Run("Should write small record as a single FULL fragment", func(t *testing.T) {
		p := []byte("hello wal")
		dst := writeAll(t, p)
		if dst.buf.Len() != headerSize+len(p) {
			t.Fatalf("got %d bytes, want %d", dst.buf.Len(), headerSize+len(p))
		}
		frags := parseFragments(t, dst.buf.Bytes())
		if len(frags) != 1 || frags[0].typ != recordTypeFull || !bytes.Equal(frags[0].payload, p) {
			t.Fatalf("got %+v, want one FULL fragment with the payload", frags)
		}
	})

	t.Run("Should pack consecutive records in the same block", func(t *testing.T) {
		a, b := []byte("first"), []byte("second")
		frags := parseFragments(t, writeAll(t, a, b).buf.Bytes())
		if len(frags) != 2 {
			t.Fatalf("got %d fragments, want 2", len(frags))
		}
		if frags[1].block != 0 || frags[1].offset != headerSize+len(a) {
			t.Fatalf("second record at block %d offset %d, want block 0 offset %d", frags[1].block, frags[1].offset, headerSize+len(a))
		}
	})

	t.Run("Should fill a block exactly with a max size record and start next record at the next block", func(t *testing.T) {
		dst := writeAll(t, payloadOf(maxPayloadSize, 1), []byte("b"))
		frags := parseFragments(t, dst.buf.Bytes())
		if !equalTypes(types(frags), []recordType{recordTypeFull, recordTypeFull}) {
			t.Fatalf("got types %v, want [FULL FULL]", types(frags))
		}
		if frags[1].block != 1 || frags[1].offset != 0 {
			t.Fatalf("second record at block %d offset %d, want block 1 offset 0", frags[1].block, frags[1].offset)
		}
		if dst.buf.Len() != blockSize+headerSize+1 {
			t.Fatalf("got %d bytes, want %d", dst.buf.Len(), blockSize+headerSize+1)
		}
	})

	t.Run("Should split record one byte larger than a block into FIRST and LAST", func(t *testing.T) {
		frags := parseFragments(t, writeAll(t, payloadOf(maxPayloadSize+1, 2)).buf.Bytes())
		if !equalTypes(types(frags), []recordType{recordTypeFirst, recordTypeLast}) {
			t.Fatalf("got types %v, want [FIRST LAST]", types(frags))
		}
		if len(frags[0].payload) != maxPayloadSize || len(frags[1].payload) != 1 {
			t.Fatalf("got sizes %d and %d, want %d and 1", len(frags[0].payload), len(frags[1].payload), maxPayloadSize)
		}
		if frags[1].block != 1 || frags[1].offset != 0 {
			t.Fatalf("LAST at block %d offset %d, want block 1 offset 0", frags[1].block, frags[1].offset)
		}
	})

	t.Run("Should split record spanning three blocks into FIRST MIDDLE LAST", func(t *testing.T) {
		frags := parseFragments(t, writeAll(t, payloadOf(2*maxPayloadSize+100, 3)).buf.Bytes())
		want := []recordType{recordTypeFirst, recordTypeMiddle, recordTypeLast}
		if !equalTypes(types(frags), want) {
			t.Fatalf("got types %v, want %v", types(frags), want)
		}
	})

	t.Run("Should start record that begins mid block with FIRST", func(t *testing.T) {
		frags := parseFragments(t, writeAll(t, []byte("pad"), payloadOf(blockSize, 4)).buf.Bytes())
		if frags[1].typ != recordTypeFirst {
			t.Fatalf("got %d for second record first fragment, want FIRST", frags[1].typ)
		}
	})

	t.Run("Should write zero length FIRST when exactly seven bytes remain", func(t *testing.T) {
		first := payloadOf(blockSize-2*headerSize, 5)
		frags := parseFragments(t, writeAll(t, first, []byte("abc")).buf.Bytes())
		want := []recordType{recordTypeFull, recordTypeFirst, recordTypeLast}
		if !equalTypes(types(frags), want) {
			t.Fatalf("got types %v, want %v", types(frags), want)
		}
		if frags[1].offset != blockSize-headerSize || len(frags[1].payload) != 0 {
			t.Fatalf("FIRST at offset %d with %d bytes, want offset %d with 0 bytes", frags[1].offset, len(frags[1].payload), blockSize-headerSize)
		}
		if frags[2].block != 1 || frags[2].offset != 0 || !bytes.Equal(frags[2].payload, []byte("abc")) {
			t.Fatalf("LAST got %+v, want block 1 offset 0 with payload abc", frags[2])
		}
	})

	t.Run("Should write empty record as zero length FULL when exactly seven bytes remain", func(t *testing.T) {
		first := payloadOf(blockSize-2*headerSize, 6)
		dst := writeAll(t, first, []byte{})
		frags := parseFragments(t, dst.buf.Bytes())
		if !equalTypes(types(frags), []recordType{recordTypeFull, recordTypeFull}) {
			t.Fatalf("got types %v, want [FULL FULL]", types(frags))
		}
		if dst.buf.Len() != blockSize {
			t.Fatalf("got %d bytes, want %d", dst.buf.Len(), blockSize)
		}
	})

	t.Run("Should pad with zeros when fewer than seven bytes remain", func(t *testing.T) {
		for leftover := 1; leftover < headerSize; leftover++ {
			first := payloadOf(blockSize-headerSize-leftover, 7)
			dst := writeAll(t, first, []byte("xyz"))
			b := dst.buf.Bytes()
			for i := blockSize - leftover; i < blockSize; i++ {
				if b[i] != 0 {
					t.Fatalf("leftover %d: byte %d is %#x, want 0", leftover, i, b[i])
				}
			}
			frags := parseFragments(t, b)
			if len(frags) != 2 || frags[1].block != 1 || frags[1].offset != 0 || frags[1].typ != recordTypeFull {
				t.Fatalf("leftover %d: got %+v, want second record as FULL at block 1 offset 0", leftover, frags)
			}
			if dst.buf.Len() != blockSize+headerSize+3 {
				t.Fatalf("leftover %d: got %d bytes, want %d", leftover, dst.buf.Len(), blockSize+headerSize+3)
			}
		}
	})

	t.Run("Should round trip many records of varied sizes", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(11, 11))
		sizes := []int{0, 1, maxPayloadSize - 1, maxPayloadSize, maxPayloadSize + 1, 3 * blockSize, 0}
		for range 200 {
			sizes = append(sizes, rng.IntN(70000))
		}
		records := make([][]byte, len(sizes))
		for i, n := range sizes {
			records[i] = payloadOf(n, byte(i))
		}
		got := assemble(t, parseFragments(t, writeAll(t, records...).buf.Bytes()))
		if len(got) != len(records) {
			t.Fatalf("got %d records, want %d", len(got), len(records))
		}
		for i := range records {
			if !bytes.Equal(got[i], records[i]) {
				t.Fatalf("record %d (size %d) differs after round trip", i, len(records[i]))
			}
		}
	})
}

func TestWriterSync(t *testing.T) {
	t.Run("Should not sync on AddRecord", func(t *testing.T) {
		dst := writeAll(t, []byte("a"), payloadOf(3*blockSize, 1))
		if dst.syncs != 0 {
			t.Fatalf("got %d syncs, want 0", dst.syncs)
		}
	})

	t.Run("Should forward Sync to destination", func(t *testing.T) {
		dst := &fakeDst{}
		w := NewWriter(dst)
		if err := w.Sync(); err != nil {
			t.Fatalf("got %v, want nil", err)
		}
		if dst.syncs != 1 {
			t.Fatalf("got %d syncs, want 1", dst.syncs)
		}
	})
}

func TestWriterStickyError(t *testing.T) {
	t.Run("Should keep returning write error and stop writing", func(t *testing.T) {
		dst := &fakeDst{failWriteAt: 1}
		w := NewWriter(dst)
		if err := w.AddRecord([]byte("a")); !errors.Is(err, errWrite) {
			t.Fatalf("first AddRecord: got %v, want %v", err, errWrite)
		}
		dst.failWriteAt = 0
		before := dst.buf.Len()
		if err := w.AddRecord([]byte("b")); !errors.Is(err, errWrite) {
			t.Fatalf("second AddRecord: got %v, want %v", err, errWrite)
		}
		if dst.buf.Len() != before {
			t.Fatalf("wrote %d bytes after failure", dst.buf.Len()-before)
		}
		if err := w.Sync(); !errors.Is(err, errWrite) {
			t.Fatalf("Sync: got %v, want %v", err, errWrite)
		}
		if dst.syncs != 0 {
			t.Fatalf("got %d syncs after failure, want 0", dst.syncs)
		}
	})

	t.Run("Should keep returning error when payload write fails after header", func(t *testing.T) {
		dst := &fakeDst{failWriteAt: 2}
		w := NewWriter(dst)
		if err := w.AddRecord([]byte("a")); !errors.Is(err, errWrite) {
			t.Fatalf("got %v, want %v", err, errWrite)
		}
		dst.failWriteAt = 0
		if err := w.AddRecord([]byte("b")); !errors.Is(err, errWrite) {
			t.Fatalf("got %v, want %v", err, errWrite)
		}
	})

	t.Run("Should keep returning error when trailer write fails", func(t *testing.T) {
		dst := &fakeDst{}
		w := NewWriter(dst)
		if err := w.AddRecord(payloadOf(blockSize-headerSize-3, 1)); err != nil {
			t.Fatalf("setup AddRecord: %v", err)
		}
		dst.failWriteAt = dst.writes + 1
		if err := w.AddRecord([]byte("b")); !errors.Is(err, errWrite) {
			t.Fatalf("got %v, want %v", err, errWrite)
		}
		dst.failWriteAt = 0
		if err := w.AddRecord([]byte("c")); !errors.Is(err, errWrite) {
			t.Fatalf("got %v, want %v", err, errWrite)
		}
	})

	t.Run("Should keep returning sync error and stop writing", func(t *testing.T) {
		dst := &fakeDst{syncErr: errSync}
		w := NewWriter(dst)
		if err := w.AddRecord([]byte("a")); err != nil {
			t.Fatalf("AddRecord: %v", err)
		}
		if err := w.Sync(); !errors.Is(err, errSync) {
			t.Fatalf("first Sync: got %v, want %v", err, errSync)
		}
		dst.syncErr = nil
		before := dst.buf.Len()
		if err := w.AddRecord([]byte("b")); !errors.Is(err, errSync) {
			t.Fatalf("AddRecord after sync failure: got %v, want %v", err, errSync)
		}
		if dst.buf.Len() != before {
			t.Fatalf("wrote %d bytes after sync failure", dst.buf.Len()-before)
		}
		if err := w.Sync(); !errors.Is(err, errSync) {
			t.Fatalf("second Sync: got %v, want %v", err, errSync)
		}
		if dst.syncs != 1 {
			t.Fatalf("got %d syncs, want 1", dst.syncs)
		}
	})
}
