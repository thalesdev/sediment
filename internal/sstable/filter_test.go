package sstable

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestFilterWriter(t *testing.T) {
	t.Run("Should write only the trailer when there are no keys", func(t *testing.T) {
		fw := newFilterWriter(10)
		fw.startBlock(0)
		want := []byte{0, 0, 0, 0, filterBaseLg}
		if got := fw.finish(); !bytes.Equal(got, want) {
			t.Fatalf("got % x, want % x", got, want)
		}
	})

	t.Run("Should create one filter per 2KB range and empty filters for skipped ranges", func(t *testing.T) {
		fw := newFilterWriter(10)
		fw.startBlock(0)
		fw.addKey([]byte("a"))
		fw.addKey([]byte("b"))
		fw.startBlock(5000)
		fw.addKey([]byte("c"))
		fb := fw.finish()

		for _, tc := range []struct {
			offset uint64
			key    string
			want   bool
		}{
			{0, "a", true}, {0, "b", true}, {4096, "c", true},
			{2048, "a", false}, {2048, "c", false},
		} {
			if got := filterMayContain(t, fb, tc.offset, []byte(tc.key)); got != tc.want {
				t.Fatalf("filter for block at %d, key %q: got %v, want %v", tc.offset, tc.key, got, tc.want)
			}
		}
		if num := (len(fb) - 5 - int(binary.LittleEndian.Uint32(fb[len(fb)-5:]))) / 4; num != 3 {
			t.Fatalf("got %d filters, want 3", num)
		}
	})

	t.Run("Should share a filter between blocks starting in the same range", func(t *testing.T) {
		fw := newFilterWriter(10)
		fw.startBlock(0)
		fw.addKey([]byte("a"))
		fw.startBlock(1000)
		fw.addKey([]byte("b"))
		fb := fw.finish()
		if !filterMayContain(t, fb, 1000, []byte("a")) || !filterMayContain(t, fb, 0, []byte("b")) {
			t.Fatal("blocks in the same 2KB range do not share a filter")
		}
	})
}
