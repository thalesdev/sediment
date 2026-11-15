// SPDX-License-Identifier: MIT

package command

import (
	"bytes"
	"testing"
	"time"
)

func TestEncodeDecodeValue(t *testing.T) {
	t.Run("Should round trip a value without a deadline", func(t *testing.T) {
		got, deadline, err := decodeValue(encodeValue(0, []byte("abc")))
		if err != nil || deadline != 0 || string(got) != "abc" {
			t.Fatalf("got %q, %d, %v", got, deadline, err)
		}
	})

	t.Run("Should round trip a value with a deadline", func(t *testing.T) {
		got, deadline, err := decodeValue(encodeValue(1234567890123, []byte("abc")))
		if err != nil || deadline != 1234567890123 || string(got) != "abc" {
			t.Fatalf("got %q, %d, %v", got, deadline, err)
		}
	})

	t.Run("Should round trip an empty value", func(t *testing.T) {
		got, _, err := decodeValue(encodeValue(5, nil))
		if err != nil || len(got) != 0 {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("Should return an error for a header that is not valid", func(t *testing.T) {
		for _, raw := range [][]byte{nil, {}, {2}, {0xff, 1, 2}, {tagDeadline}, {tagDeadline, 1, 2, 3}, encodeValue(0, nil)[:0]} {
			if _, _, err := decodeValue(raw); err == nil {
				t.Fatalf("no error for %v", raw)
			}
		}
		zero := append([]byte{tagDeadline}, make([]byte, deadlineLen)...)
		if _, _, err := decodeValue(zero); err == nil {
			t.Fatal("no error for a zero deadline")
		}
		negative := append([]byte{tagDeadline}, bytes.Repeat([]byte{0xff}, deadlineLen)...)
		if _, _, err := decodeValue(negative); err == nil {
			t.Fatal("no error for a negative deadline")
		}
	})
}

func TestExpiredFilter(t *testing.T) {
	now := time.UnixMilli(1000)
	filter := ExpiredFilter(func() time.Time { return now })

	t.Run("Should drop a value whose deadline has passed", func(t *testing.T) {
		if !filter([]byte("k"), encodeValue(1000, []byte("v"))) {
			t.Fatal("the value at the deadline should be dropped")
		}
		if !filter([]byte("k"), encodeValue(1, []byte("v"))) {
			t.Fatal("an old value should be dropped")
		}
	})

	t.Run("Should keep a value that is alive or has no deadline", func(t *testing.T) {
		if filter([]byte("k"), encodeValue(1001, []byte("v"))) {
			t.Fatal("a live value should be kept")
		}
		if filter([]byte("k"), encodeValue(0, []byte("v"))) {
			t.Fatal("a value without a deadline should be kept")
		}
	})

	t.Run("Should keep a value that it cannot read", func(t *testing.T) {
		if filter([]byte("k"), []byte{9, 9, 9}) || filter([]byte("k"), nil) {
			t.Fatal("a value with a bad header should be kept")
		}
	})
}

func FuzzDecodeValue(f *testing.F) {
	f.Add([]byte{})
	f.Add(encodeValue(0, []byte("v")))
	f.Add(encodeValue(99, []byte("v")))
	f.Add([]byte{tagDeadline, 1})
	f.Fuzz(func(t *testing.T, raw []byte) {
		value, deadline, err := decodeValue(raw)
		if err != nil {
			return
		}
		if deadline < 0 {
			t.Fatalf("negative deadline %d", deadline)
		}
		if !bytes.Equal(encodeValue(deadline, value), raw) {
			t.Fatalf("round trip changed %v", raw)
		}
	})
}
