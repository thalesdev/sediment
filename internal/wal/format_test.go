// SPDX-License-Identifier: MIT

package wal

import (
	"bytes"
	"hash/crc32"
	"testing"
)

func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic, got none")
		}
	}()
	fn()
}

func filled(n int, f func(i int) byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = f(i)
	}
	return b
}

func TestCRC32C(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want uint32
	}{
		{"Should match RFC 3720 vector for 32 zero bytes", filled(32, func(int) byte { return 0x00 }), 0x8a9136aa},
		{"Should match RFC 3720 vector for 32 0xff bytes", filled(32, func(int) byte { return 0xff }), 0x62a8ab43},
		{"Should match RFC 3720 vector for 32 ascending bytes", filled(32, func(i int) byte { return byte(i) }), 0x46dd794e},
		{"Should match RFC 3720 vector for 32 descending bytes", filled(32, func(i int) byte { return byte(31 - i) }), 0x113fdb5c},
		{"Should match CRC-32C check value for 123456789", []byte("123456789"), 0xe3069283},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := crc32.Checksum(tc.data, crcTable); got != tc.want {
				t.Fatalf("got %#08x, want %#08x", got, tc.want)
			}
		})
	}
}

func TestMask(t *testing.T) {
	values := []uint32{0, 1, 0xffffffff, 0x12345678, 0xa282ead8, crc32.Checksum([]byte("foo"), crcTable)}

	t.Run("Should restore the original value after unmask", func(t *testing.T) {
		for _, v := range values {
			if got := unmaskChecksum(maskChecksum(v)); got != v {
				t.Fatalf("unmask(mask(%#08x)) = %#08x", v, got)
			}
		}
	})

	t.Run("Should change the value when masking", func(t *testing.T) {
		for _, v := range values {
			if maskChecksum(v) == v {
				t.Fatalf("mask(%#08x) returned the same value", v)
			}
		}
	})
}

func TestHeader(t *testing.T) {
	types := []recordType{recordTypeFull, recordTypeFirst, recordTypeMiddle, recordTypeLast}
	payloads := map[string][]byte{
		"empty": {},
		"small": []byte("hello wal"),
		"max":   filled(maxPayloadSize, func(i int) byte { return byte(i * 7) }),
	}

	t.Run("Should round trip through marshal and unmarshal", func(t *testing.T) {
		for _, rt := range types {
			for name, p := range payloads {
				h := newHeader(p, rt)
				buf := make([]byte, headerSize)
				h.marshal(buf)

				var got header
				got.unmarshal(buf)
				if got != h {
					t.Fatalf("type %d, payload %s: got %+v, want %+v", rt, name, got, h)
				}
			}
		}
	})

	t.Run("Should derive length from payload", func(t *testing.T) {
		for name, p := range payloads {
			h := newHeader(p, recordTypeFull)
			if int(h.length) != len(p) {
				t.Fatalf("payload %s: got length %d, want %d", name, h.length, len(p))
			}
		}
	})

	t.Run("Should compute masked CRC-32C over type followed by payload", func(t *testing.T) {
		for _, rt := range types {
			for name, p := range payloads {
				joined := append([]byte{byte(rt)}, p...)
				want := maskChecksum(crc32.Checksum(joined, crcTable))
				if got := newHeader(p, rt).checksum; got != want {
					t.Fatalf("type %d, payload %s: got %#08x, want %#08x", rt, name, got, want)
				}
			}
		}
	})

	t.Run("Should produce different checksums for different types with same payload", func(t *testing.T) {
		p := []byte("same payload")
		seen := map[uint32]recordType{}
		for _, rt := range types {
			c := newHeader(p, rt).checksum
			if prev, ok := seen[c]; ok {
				t.Fatalf("types %d and %d produced the same checksum %#08x", prev, rt, c)
			}
			seen[c] = rt
		}
	})

	t.Run("Should encode fields in little-endian at fixed offsets", func(t *testing.T) {
		h := header{checksum: 0x01020304, length: 0x0506, recordType: recordTypeFull}
		buf := make([]byte, headerSize)
		h.marshal(buf)
		want := []byte{0x04, 0x03, 0x02, 0x01, 0x06, 0x05, 0x01}
		if !bytes.Equal(buf, want) {
			t.Fatalf("got % x, want % x", buf, want)
		}
	})

	t.Run("Should panic when payload exceeds max payload size", func(t *testing.T) {
		mustPanic(t, func() { newHeader(make([]byte, maxPayloadSize+1), recordTypeFull) })
	})

	t.Run("Should panic on unknown record type", func(t *testing.T) {
		mustPanic(t, func() { newHeader([]byte("x"), recordType(5)) })
	})

	t.Run("Should panic on zero record type", func(t *testing.T) {
		mustPanic(t, func() { newHeader([]byte("x"), recordTypeZero) })
	})

	t.Run("Should panic when marshal buffer is too small", func(t *testing.T) {
		h := newHeader([]byte("x"), recordTypeFull)
		mustPanic(t, func() { h.marshal(make([]byte, headerSize-1)) })
	})

	t.Run("Should panic when unmarshal buffer is too small", func(t *testing.T) {
		var h header
		mustPanic(t, func() { h.unmarshal(make([]byte, headerSize-1)) })
	})
}
