// SPDX-License-Identifier: MIT

package command

import (
	"bytes"
	"testing"
	"time"
)

func TestEncodeDecodeRecord(t *testing.T) {
	t.Run("Should round trip a string without a deadline", func(t *testing.T) {
		r, err := decodeRecord(encodeRecord(typeString, 0, []byte("abc")))
		if err != nil || r.typ != typeString || r.deadline != 0 || string(r.body) != "abc" {
			t.Fatalf("got %+v, %v", r, err)
		}
	})

	t.Run("Should round trip a string with a deadline", func(t *testing.T) {
		r, err := decodeRecord(encodeRecord(typeString, 1234567890123, []byte("abc")))
		if err != nil || r.deadline != 1234567890123 || string(r.body) != "abc" {
			t.Fatalf("got %+v, %v", r, err)
		}
	})

	t.Run("Should round trip an empty string", func(t *testing.T) {
		r, err := decodeRecord(encodeRecord(typeString, 5, nil))
		if err != nil || len(r.body) != 0 {
			t.Fatalf("got %+v, %v", r, err)
		}
	})

	t.Run("Should round trip a hash and a set", func(t *testing.T) {
		for _, typ := range []valueType{typeHash, typeSet} {
			r, err := decodeRecord(encodeRecord(typ, 77, collectionBody(9, 4)))
			if err != nil || r.typ != typ || r.deadline != 77 {
				t.Fatalf("got %+v, %v", r, err)
			}
			if uid, count := r.collection(); uid != 9 || count != 4 {
				t.Fatalf("uid %d count %d", uid, count)
			}
		}
	})

	t.Run("Should return an error for a header that is not valid", func(t *testing.T) {
		bad := [][]byte{
			nil,
			{},
			{0x20},
			{0x80},
			{0x03},
			{0x0f, 1},
			{hdrHasDeadline},
			{hdrHasDeadline, 1, 2, 3},
			{byte(typeHash)},
			append([]byte{byte(typeHash)}, make([]byte, 15)...),
			append([]byte{byte(typeSet)}, make([]byte, 17)...),
			append([]byte{hdrHasDeadline}, make([]byte, deadlineLen)...),
			append([]byte{hdrHasDeadline}, bytes.Repeat([]byte{0xff}, deadlineLen)...),
		}
		for _, raw := range bad {
			if _, err := decodeRecord(raw); err == nil {
				t.Fatalf("no error for % x", raw)
			}
		}
	})
}

func TestExpiredFilter(t *testing.T) {
	now := time.UnixMilli(1000)
	filter := ExpiredFilter(func() time.Time { return now })
	key := keyOf([]byte("k"))

	t.Run("Should drop a KEY record whose deadline has passed", func(t *testing.T) {
		if !filter(key, encodeRecord(typeString, 1000, []byte("v"))) {
			t.Fatal("the record at the deadline should be dropped")
		}
		if !filter(key, encodeRecord(typeHash, 1, collectionBody(1, 1))) {
			t.Fatal("an old hash should be dropped")
		}
	})

	t.Run("Should keep a record that is alive or has no deadline", func(t *testing.T) {
		if filter(key, encodeRecord(typeString, 1001, []byte("v"))) {
			t.Fatal("a live record should be kept")
		}
		if filter(key, encodeRecord(typeString, 0, []byte("v"))) {
			t.Fatal("a record without a deadline should be kept")
		}
	})

	t.Run("Should keep a record that it cannot read", func(t *testing.T) {
		if filter(key, []byte{9, 9, 9}) || filter(key, nil) {
			t.Fatal("a record with a bad header should be kept")
		}
	})

	t.Run("Should keep every record that is not a KEY record", func(t *testing.T) {
		expired := encodeRecord(typeString, 1, []byte("v"))
		for _, k := range [][]byte{elementKey(1, 2, 0, []byte("f")), ownerKey(1, 2), systemKey("format"), nil} {
			if filter(k, expired) {
				t.Fatalf("a record of key % x should be kept", k)
			}
		}
	})
}

func FuzzDecodeRecord(f *testing.F) {
	f.Add([]byte{})
	f.Add(encodeRecord(typeString, 0, []byte("v")))
	f.Add(encodeRecord(typeString, 99, []byte("v")))
	f.Add(encodeRecord(typeHash, 5, collectionBody(7, 8)))
	f.Add([]byte{hdrHasDeadline, 1})
	f.Fuzz(func(t *testing.T, raw []byte) {
		r, err := decodeRecord(raw)
		if err != nil {
			return
		}
		if r.deadline < 0 {
			t.Fatalf("negative deadline %d", r.deadline)
		}
		if !bytes.Equal(encodeRecord(r.typ, r.deadline, r.body), raw) {
			t.Fatalf("round trip changed % x", raw)
		}
		if r.typ != typeString {
			r.collection()
		}
	})
}
