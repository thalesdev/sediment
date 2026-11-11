// SPDX-License-Identifier: MIT

package bloom

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"testing"
)

func keyN(prefix string, i int) []byte {
	return fmt.Appendf(nil, "%s-%08d", prefix, i)
}

func keys(prefix string, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = keyN(prefix, i)
	}
	return out
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHash(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want uint32
	}{
		{"Should return the seed for empty input", []byte{}, 0xbc9f1d34},
		{"Should match LevelDB vector for 1 byte", []byte{0x62}, 0xef1345c4},
		{"Should match LevelDB vector for 2 bytes", []byte{0xc3, 0x97}, 0x5b663814},
		{"Should match LevelDB vector for 3 bytes", []byte{0xe2, 0x99, 0xa5}, 0x323c078f},
		{"Should match LevelDB vector for 4 bytes", []byte{0xe1, 0x80, 0xb9, 0x32}, 0xed21633a},
		{"Should match goleveldb for 5 bytes", []byte("hello"), 0xf795964e},
		{"Should match goleveldb for 9 bytes", []byte("123456789"), 0x73dddd2a},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hash(tc.data); got != tc.want {
				t.Fatalf("got %#08x, want %#08x", got, tc.want)
			}
		})
	}
}

func TestFilterFormat(t *testing.T) {
	t.Run("Should match goleveldb bytes for two keys", func(t *testing.T) {
		got := New([][]byte{[]byte("hello"), []byte("world")}, 10)
		want := mustHex(t, "114000414410401006")
		if !bytes.Equal(got, want) {
			t.Fatalf("got %x, want %x", []byte(got), want)
		}
	})

	t.Run("Should match goleveldb bytes for twenty keys", func(t *testing.T) {
		var ks [][]byte
		for i := range 20 {
			ks = append(ks, fmt.Appendf(nil, "key-%02d", i))
		}
		got := New(ks, 10)
		want := mustHex(t, "24f4ce50051755b7fd8906690ac7243c25751b24c5d892e37c06")
		if !bytes.Equal(got, want) {
			t.Fatalf("got %x, want %x", []byte(got), want)
		}
	})

	t.Run("Should use at least 64 bits", func(t *testing.T) {
		f := New([][]byte{[]byte("a")}, 10)
		if len(f) != 64/8+1 {
			t.Fatalf("got %d bytes, want %d", len(f), 64/8+1)
		}
	})

	t.Run("Should size the bit array as keys times bits per key rounded up to bytes", func(t *testing.T) {
		f := New(keys("k", 1001), 10)
		if want := (1001*10+7)/8 + 1; len(f) != want {
			t.Fatalf("got %d bytes, want %d", len(f), want)
		}
	})

	t.Run("Should store k in the last byte", func(t *testing.T) {
		tests := map[int]int{1: 1, 2: 1, 10: 6, 20: 13, 100: 30}
		for bpk, want := range tests {
			f := New(keys("k", 10), bpk)
			if got := int(f[len(f)-1]); got != want {
				t.Fatalf("bits per key %d: got k=%d, want %d", bpk, got, want)
			}
		}
	})

	t.Run("Should use the default bits per key when given less than one", func(t *testing.T) {
		if !bytes.Equal(New(keys("k", 50), 0), New(keys("k", 50), DefaultBitsPerKey)) {
			t.Fatal("filter with 0 bits per key differs from the default")
		}
	})
}

func TestMayContain(t *testing.T) {
	t.Run("Should never return false for an inserted key", func(t *testing.T) {
		for _, n := range []int{1, 10, 100, 10000} {
			ks := keys("in", n)
			f := New(ks, 10)
			for _, k := range ks {
				if !f.MayContain(k) {
					t.Fatalf("n=%d: false negative for %q", n, k)
				}
			}
		}
	})

	t.Run("Should return false for any key on an empty or one byte filter", func(t *testing.T) {
		for _, f := range []Filter{nil, {}, {0x06}} {
			if f.MayContain([]byte("x")) {
				t.Fatalf("filter %x returned true", []byte(f))
			}
		}
	})

	t.Run("Should return true for any key when k is reserved", func(t *testing.T) {
		f := Filter{0, 0, 0, 0, 0, 0, 0, 0, 31}
		if !f.MayContain([]byte("anything")) {
			t.Fatal("filter with k=31 returned false")
		}
	})

	t.Run("Should reject every key when built from no keys", func(t *testing.T) {
		f := New(nil, 10)
		hits := 0
		for _, k := range keys("out", 1000) {
			if f.MayContain(k) {
				hits++
			}
		}
		if hits != 0 {
			t.Fatalf("got %d hits on an empty filter, want 0", hits)
		}
	})

	t.Run("Should have false positive rate close to theory", func(t *testing.T) {
		const n, probes = 10000, 100000
		for _, bpk := range []int{6, 10, 16} {
			f := New(keys("in", n), bpk)
			fp := 0
			for i := range probes {
				if f.MayContain(keyN("out", i)) {
					fp++
				}
			}
			got := float64(fp) / probes
			numHashes := float64(k(bpk))
			want := math.Pow(1-math.Exp(-numHashes/float64(bpk)), numHashes)
			if got > want*1.3+0.001 || got < want*0.7-0.001 {
				t.Fatalf("bits per key %d: false positive rate %.4f, theory %.4f", bpk, got, want)
			}
		}
	})
}
