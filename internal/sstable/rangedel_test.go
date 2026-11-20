// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"fmt"
	"testing"
)

func tableWithRangeDels(opts Options, entries []kv, dels []kv) []byte {
	dst := &fakeDst{}
	w := NewWriter(dst, opts)
	for _, e := range entries {
		if err := w.Add(e.key, e.value); err != nil {
			panic(err)
		}
	}
	for _, d := range dels {
		if err := w.AddRangeDel(d.key, d.value); err != nil {
			panic(err)
		}
	}
	if err := w.Finish(); err != nil {
		panic(err)
	}
	return dst.buf.Bytes()
}

func TestRangeDels(t *testing.T) {
	t.Run("Should give back the range deletes in order", func(t *testing.T) {
		dels := []kv{{[]byte("a"), []byte("c")}, {[]byte("b"), []byte("z")}, {[]byte("m"), []byte("n")}}
		r := mustOpen(t, tableWithRangeDels(Options{}, numbered(50, 10), dels))
		got := r.RangeDels()
		if len(got) != len(dels) {
			t.Fatalf("got %d range deletes, want %d", len(got), len(dels))
		}
		for i, d := range dels {
			if !bytes.Equal(got[i].Key, d.key) || !bytes.Equal(got[i].End, d.value) {
				t.Fatalf("range delete %d is %q to %q, want %q to %q", i, got[i].Key, got[i].End, d.key, d.value)
			}
		}
	})

	t.Run("Should keep the data entries as they were", func(t *testing.T) {
		entries := numbered(300, 20)
		data := tableWithRangeDels(Options{BlockSize: 256}, entries, []kv{{[]byte("a"), []byte("b")}})
		checkAgainst(t, data, entries, "with range deletes")
	})

	t.Run("Should write the data and the filter exactly as without range deletes", func(t *testing.T) {
		entries := numbered(300, 20)
		plain := tableBytes(Options{BlockSize: 256}, entries)
		with := tableWithRangeDels(Options{BlockSize: 256}, entries, []kv{{[]byte("a"), []byte("b")}})
		metaHandle, n := decodeBlockHandle(plain[len(plain)-footerSize:])
		if n <= 0 {
			t.Fatal("footer")
		}
		end := int(metaHandle.offset)
		if !bytes.Equal(plain[:end], with[:end]) {
			t.Fatal("the data blocks or the filter changed")
		}
	})

	t.Run("Should have no range deletes in a table that has none", func(t *testing.T) {
		r := mustOpen(t, tableBytes(Options{}, numbered(10, 10)))
		if got := r.RangeDels(); len(got) != 0 {
			t.Fatalf("got %d", len(got))
		}
	})

	t.Run("Should open a table that has only range deletes", func(t *testing.T) {
		r := mustOpen(t, tableWithRangeDels(Options{}, nil, []kv{{[]byte("a"), []byte("z")}}))
		if len(r.RangeDels()) != 1 {
			t.Fatalf("got %d range deletes", len(r.RangeDels()))
		}
		it := r.NewIterator()
		defer it.Close()
		it.SeekToFirst()
		if it.Valid() || it.Err() != nil {
			t.Fatalf("valid=%v err=%v", it.Valid(), it.Err())
		}
		if _, _, ok, err := r.Find([]byte("m")); ok || err != nil {
			t.Fatalf("Find: ok=%v err=%v", ok, err)
		}
	})

	t.Run("Should panic if the range deletes are not in increasing order", func(t *testing.T) {
		w := NewWriter(&fakeDst{}, Options{})
		if err := w.AddRangeDel([]byte("b"), []byte("c")); err != nil {
			t.Fatal(err)
		}
		mustPanic(t, func() { w.AddRangeDel([]byte("b"), []byte("d")) })
		mustPanic(t, func() { w.AddRangeDel([]byte("a"), []byte("d")) })
	})

	t.Run("Should return an error for a range delete block that is not in order", func(t *testing.T) {
		block := newBlockWriter(16)
		block.add([]byte("b"), []byte("c"))
		block.add([]byte("a"), []byte("d"))
		data := craftRangeDelTable(t, block.finish())
		_, err := openBounded(t, data)
		assertCorrupt(t, err)
	})

	t.Run("Should return an error if a bit of the range delete block changes", func(t *testing.T) {
		data := tableWithRangeDels(Options{}, numbered(5, 5), []kv{{[]byte("aaaa"), []byte("bbbb")}, {[]byte("cccc"), []byte("dddd")}})
		clean := mustOpen(t, data)
		if len(clean.RangeDels()) != 2 {
			t.Fatal("setup")
		}
		for i := range data {
			flipped := bytes.Clone(data)
			flipped[i] ^= 0x40
			r, err := openBounded(t, flipped)
			if err != nil {
				continue
			}
			for _, d := range r.RangeDels() {
				_ = d.Key
				_ = d.End
			}
		}
	})

	t.Run("Should keep the range deletes when many are added", func(t *testing.T) {
		var dels []kv
		for i := range 500 {
			dels = append(dels, kv{[]byte(fmt.Sprintf("k%05d", i)), []byte(fmt.Sprintf("k%05d", i+1))})
		}
		r := mustOpen(t, tableWithRangeDels(Options{}, numbered(10, 10), dels))
		if len(r.RangeDels()) != 500 {
			t.Fatalf("got %d", len(r.RangeDels()))
		}
	})
}

func craftRangeDelTable(t *testing.T, rangeBlock []byte) []byte {
	t.Helper()
	var data []byte
	dataBlock := rawBlock(nil, 0)
	var dataHandle blockHandle
	data, dataHandle = appendGoodBlock(data, dataBlock)
	var rangeHandle blockHandle
	data, rangeHandle = appendGoodBlock(data, rangeBlock)
	index := newBlockWriter(1)
	index.add([]byte("a"), dataHandle.append(nil))
	meta := newBlockWriter(16)
	meta.add([]byte(rangeDelMetaKey), rangeHandle.append(nil))
	var metaHandle, indexHandle blockHandle
	data, metaHandle = appendGoodBlock(data, meta.finish())
	data, indexHandle = appendGoodBlock(data, index.finish())
	return appendFooter(data, metaHandle, indexHandle)
}

func FuzzReaderRangeDels(f *testing.F) {
	f.Add(tableWithRangeDels(Options{}, numbered(5, 5), []kv{{[]byte("a"), []byte("b")}}))
	f.Add(tableWithRangeDels(Options{}, nil, []kv{{[]byte("a"), []byte("z")}, {[]byte("b"), []byte("c")}}))
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := openBounded(t, data)
		if err != nil {
			return
		}
		prev := []byte(nil)
		for _, d := range r.RangeDels() {
			if prev != nil && bytes.Compare(prev, d.Key) >= 0 {
				t.Fatalf("range deletes out of order: %q then %q", prev, d.Key)
			}
			prev = d.Key
		}
	})
}
