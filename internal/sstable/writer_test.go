// SPDX-License-Identifier: MIT

package sstable

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
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

func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}

const goleveldbTable = "0005076170706c65762d6170706c6505010874762d6170706c65740005076170706c79762d6170706c790205097269636f74762d61707269636f7400060862616e616e61762d62616e616e61000000001b0000003b0000000300000000c1d251ec4655006ea007d10f0600000000090000000b0044fe724b00210266696c7465722e6c6576656c64622e4275696c74696e426c6f6f6d46696c74657261120000000001000000003266b49600010263005c00000000010000000087089e40782eab010e000000000000000000000000000000000000000000000000000000000000000000000057fb808b247547db"

func TestWriter(t *testing.T) {
	t.Run("Should match goleveldb bytes for data, filter and metaindex blocks", func(t *testing.T) {
		var entries []kv
		for _, k := range []string{"apple", "applet", "apply", "apricot", "banana"} {
			entries = append(entries, kv{[]byte(k), []byte("v-" + k)})
		}
		got := writeTable(t, Options{RestartInterval: 2}, entries)
		want, _ := hex.DecodeString(goleveldbTable)

		footer := got[len(got)-footerSize:]
		_, n := decodeBlockHandle(footer)
		index, _ := decodeBlockHandle(footer[n:])
		if !bytes.Equal(got[:index.offset], want[:index.offset]) {
			t.Fatalf("got\n%x\nwant prefix\n%x", got[:index.offset], want[:index.offset])
		}
	})

	for _, tc := range []struct {
		name    string
		entries []kv
	}{
		{"empty", nil},
		{"single", []kv{{[]byte("abobrinha"), []byte("verde")}}},
		{"many small", numbered(5000, 8)},
		{"values larger than a block", numbered(30, 9000)},
	} {
		t.Run("Should round trip "+tc.name+" table", func(t *testing.T) {
			b := writeTable(t, Options{}, tc.entries)
			p := parseTable(t, b)
			assertEntries(t, p.entries, tc.entries)
			i := 0
			for _, h := range p.dataBlocks {
				entries, _ := decodeBlock(t, readBlock(t, b, h))
				for range entries {
					if !filterMayContain(t, p.filter, h.offset, tc.entries[i].key) {
						t.Fatalf("filter misses key %q", tc.entries[i].key)
					}
					i++
				}
			}
		})
	}

	t.Run("Should close data blocks once they reach the block size", func(t *testing.T) {
		b := writeTable(t, Options{BlockSize: 4096}, numbered(5000, 8))
		p := parseTable(t, b)
		if len(p.dataBlocks) < 2 {
			t.Fatalf("got %d data blocks, want several", len(p.dataBlocks))
		}
		for i, h := range p.dataBlocks[:len(p.dataBlocks)-1] {
			if h.size < 4096 || h.size > 4096+64 {
				t.Fatalf("data block %d has %d bytes, want just over 4096", i, h.size)
			}
		}
	})

	t.Run("Should build the filter from FilterKey", func(t *testing.T) {
		suffix := []byte{0, 0, 0, 0, 0, 0, 0, 1}
		var entries []kv
		for i := range 300 {
			entries = append(entries, kv{append(fmt.Appendf(nil, "user-%04d", i), suffix...), nil})
		}
		opts := Options{FilterKey: func(k []byte) []byte { return k[:len(k)-8] }}
		b := writeTable(t, opts, entries)
		p := parseTable(t, b)
		i := 0
		for _, h := range p.dataBlocks {
			entries, _ := decodeBlock(t, readBlock(t, b, h))
			for range entries {
				if user := fmt.Appendf(nil, "user-%04d", i); !filterMayContain(t, p.filter, h.offset, user) {
					t.Fatalf("filter misses user key %q", user)
				}
				i++
			}
		}
	})

	t.Run("Should panic on keys out of order", func(t *testing.T) {
		w := NewWriter(&fakeDst{}, Options{})
		w.Add([]byte("b"), nil)
		mustPanic(t, func() { w.Add([]byte("a"), nil) })
	})

	t.Run("Should panic on duplicate keys", func(t *testing.T) {
		w := NewWriter(&fakeDst{}, Options{})
		w.Add([]byte("a"), nil)
		mustPanic(t, func() { w.Add([]byte("a"), nil) })
	})

	t.Run("Should sync once on Finish and never on Add", func(t *testing.T) {
		dst := &fakeDst{}
		w := NewWriter(dst, Options{})
		for _, e := range numbered(3000, 8) {
			w.Add(e.key, e.value)
		}
		if dst.syncs != 0 {
			t.Fatalf("got %d syncs before Finish, want 0", dst.syncs)
		}
		if err := w.Finish(); err != nil {
			t.Fatal(err)
		}
		if dst.syncs != 1 {
			t.Fatalf("got %d syncs, want 1", dst.syncs)
		}
	})

	t.Run("Should reject Add and Finish after Finish", func(t *testing.T) {
		dst := &fakeDst{}
		w := NewWriter(dst, Options{})
		w.Add([]byte("a"), nil)
		if err := w.Finish(); err != nil {
			t.Fatal(err)
		}
		size := dst.buf.Len()
		if err := w.Add([]byte("b"), nil); !errors.Is(err, errFinished) {
			t.Fatalf("Add: got %v, want %v", err, errFinished)
		}
		if err := w.Finish(); !errors.Is(err, errFinished) {
			t.Fatalf("Finish: got %v, want %v", err, errFinished)
		}
		if dst.buf.Len() != size || dst.syncs != 1 {
			t.Fatal("writer touched dst after Finish")
		}
	})

	t.Run("Should keep returning write error and stop writing", func(t *testing.T) {
		dst := &fakeDst{failWriteAt: 3}
		w := NewWriter(dst, Options{})
		var err error
		for _, e := range numbered(5000, 8) {
			if err = w.Add(e.key, e.value); err != nil {
				break
			}
		}
		if !errors.Is(err, errWrite) {
			t.Fatalf("Add: got %v, want %v", err, errWrite)
		}
		dst.failWriteAt = 0
		size := dst.buf.Len()
		if err := w.Add([]byte("zzz"), nil); !errors.Is(err, errWrite) {
			t.Fatalf("Add after failure: got %v, want %v", err, errWrite)
		}
		if err := w.Finish(); !errors.Is(err, errWrite) {
			t.Fatalf("Finish after failure: got %v, want %v", err, errWrite)
		}
		if dst.buf.Len() != size || dst.syncs != 0 {
			t.Fatal("writer touched dst after failure")
		}
	})

	t.Run("Should keep returning sync error", func(t *testing.T) {
		dst := &fakeDst{syncErr: errSync}
		w := NewWriter(dst, Options{})
		w.Add([]byte("a"), nil)
		if err := w.Finish(); !errors.Is(err, errSync) {
			t.Fatalf("got %v, want %v", err, errSync)
		}
		dst.syncErr = nil
		if err := w.Finish(); !errors.Is(err, errSync) {
			t.Fatalf("second Finish: got %v, want %v", err, errSync)
		}
	})
}
