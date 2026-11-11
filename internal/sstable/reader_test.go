// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"
)

type countingReaderAt struct {
	b     []byte
	reads []int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.reads = append(c.reads, off)
	if off >= int64(len(c.b)) {
		return 0, io.EOF
	}
	n := copy(p, c.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func openBytes(t *testing.T, b []byte) *Reader {
	t.Helper()
	r, err := Open(bytes.NewReader(b), int64(len(b)), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r
}

func iterate(t *testing.T, it *Iterator) []kv {
	t.Helper()
	var out []kv
	for ; it.Valid(); it.Next() {
		out = append(out, kv{bytes.Clone(it.Key()), bytes.Clone(it.Value())})
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	return out
}

func tableCases() map[string][]kv {
	return map[string][]kv{
		"empty":        nil,
		"single":       numbered(1, 4),
		"many":         numbered(3000, 10),
		"large values": numbered(60, 9000),
		"empty values": numbered(500, 0),
	}
}

func TestReader(t *testing.T) {
	for name, entries := range tableCases() {
		t.Run("Should get and iterate: "+name, func(t *testing.T) {
			r := openBytes(t, writeTable(t, Options{}, entries))
			for _, e := range entries {
				v, ok, err := r.Get(e.key)
				if err != nil || !ok || !bytes.Equal(v, e.value) {
					t.Fatalf("Get(%q) = %v, %v, %v", e.key, v, ok, err)
				}
			}
			for i := range 100 {
				for _, k := range []string{fmt.Sprintf("key-%08d-x", i), fmt.Sprintf("zzz-%d", i), "a"} {
					if _, ok, err := r.Get([]byte(k)); ok || err != nil {
						t.Fatalf("Get(%q) = %v, %v", k, ok, err)
					}
				}
			}

			it := r.NewIterator()
			it.SeekToFirst()
			assertEntries(t, iterate(t, it), entries)

			for i := 0; i < len(entries); i += 1 + len(entries)/50 {
				it.Seek(entries[i].key)
				assertEntries(t, iterate(t, it), entries[i:])
				it.Seek(append(bytes.Clone(entries[i].key), 0))
				assertEntries(t, iterate(t, it), entries[i+1:])
			}
			it.Seek([]byte("zzzz"))
			if it.Valid() || it.Err() != nil {
				t.Fatalf("Seek past the end: valid=%v err=%v", it.Valid(), it.Err())
			}
			if err := it.Close(); err != nil || it.Close() != nil {
				t.Fatal("Close must be idempotent")
			}
			it.SeekToFirst()
			if it.Valid() || it.Err() == nil {
				t.Fatal("using a closed iterator must report an error")
			}
		})
	}

	t.Run("Should read the table written by goleveldb", func(t *testing.T) {
		b := mustHex(t, goleveldbTable)
		r := openBytes(t, b)
		it := r.NewIterator()
		it.SeekToFirst()
		got := iterate(t, it)
		if len(got) != 5 || string(got[0].key) != "apple" || string(got[4].key) != "banana" {
			t.Fatalf("unexpected entries %q", got)
		}
		if v, ok, _ := r.Get([]byte("apricot")); !ok || string(v) != "v-apricot" {
			t.Fatalf("Get(apricot) = %q, %v", v, ok)
		}
	})

	t.Run("Should read one data block per hit and none when the filter rejects", func(t *testing.T) {
		entries := numbered(3000, 10)
		src := &countingReaderAt{b: writeTable(t, Options{}, entries)}
		r, err := Open(src, int64(len(src.b)), Options{})
		if err != nil {
			t.Fatal(err)
		}
		src.reads = nil
		if _, ok, err := r.Get(entries[1234].key); !ok || err != nil {
			t.Fatalf("Get: %v %v", ok, err)
		}
		if len(src.reads) != 1 {
			t.Fatalf("hit read %d blocks, want 1", len(src.reads))
		}
		src.reads = nil
		rejected := 0
		for i := range 500 {
			before := len(src.reads)
			if _, ok, _ := r.Get(fmt.Appendf(nil, "key-%08d-x", i*5)); ok {
				t.Fatal("found a missing key")
			}
			if len(src.reads) == before {
				rejected++
			}
		}
		if rejected < 450 {
			t.Fatalf("filter avoided I/O on only %d of 500 misses", rejected)
		}
	})

	t.Run("Should report corruption instead of panicking", func(t *testing.T) {
		entries := numbered(3000, 10)
		good := writeTable(t, Options{}, entries)
		for pos := 0; pos < len(good); pos += 7 {
			b := bytes.Clone(good)
			b[pos] ^= 0x40
			r, err := Open(bytes.NewReader(b), int64(len(b)), Options{})
			if err != nil {
				if !errors.Is(err, errCorrupt) {
					t.Fatalf("flip at %d: Open: %v", pos, err)
				}
				continue
			}
			it := r.NewIterator()
			it.SeekToFirst()
			n := 0
			for ; it.Valid(); it.Next() {
				n++
			}
			failed := it.Err() != nil
			for _, e := range entries[:50] {
				if _, _, err := r.Get(e.key); err != nil {
					failed = true
				}
			}
			if _, _, err := r.Get(entries[2999].key); err != nil {
				failed = true
			}
			if n == len(entries) && !failed && pos < len(good)-footerSize {
				t.Fatalf("flip at %d went unnoticed", pos)
			}
		}
	})

	t.Run("Should reject truncated tables and bad magic", func(t *testing.T) {
		good := writeTable(t, Options{}, numbered(100, 4))
		for _, n := range []int{0, 10, footerSize - 1, len(good) - 1, len(good) / 2} {
			if _, err := Open(bytes.NewReader(good[:n]), int64(n), Options{}); err == nil {
				t.Fatalf("Open accepted a table truncated to %d bytes", n)
			}
		}
	})
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
