// SPDX-License-Identifier: MIT

package command

import (
	"bytes"
	"testing"

	"github.com/thalesdev/sediment/internal/shale"
)

func TestSlot(t *testing.T) {
	t.Run("Should compute the CRC-16 of Redis Cluster", func(t *testing.T) {
		if got := crc16([]byte("123456789")); got != 0x31c3 {
			t.Fatalf("crc16 = %#x, want 0x31c3", got)
		}
	})

	t.Run("Should give the slots that Redis gives", func(t *testing.T) {
		for key, want := range map[string]uint16{
			"foo": 12182, "somekey": 11058, "user1000": 3443, "a": 15495, " ": 9314,
		} {
			if got := slotOf([]byte(key)); got != want {
				t.Fatalf("slotOf(%q) = %d, want %d", key, got, want)
			}
		}
	})

	t.Run("Should use the hash tag when it has a byte", func(t *testing.T) {
		for key, want := range map[string]uint16{
			"{user1000}.following": 3443, "{user1000}.followers": 3443,
			"{}foo": 9500, "{foo": 13308, "foo{bar}": 5061, "foo{}{bar}": 8363,
		} {
			if got := slotOf([]byte(key)); got != want {
				t.Fatalf("slotOf(%q) = %d, want %d", key, got, want)
			}
		}
	})

	t.Run("Should keep every slot below the count", func(t *testing.T) {
		for i := range 5000 {
			if s := slotOf([]byte{byte(i), byte(i >> 8), 'x'}); s >= slotCount {
				t.Fatalf("slot %d", s)
			}
		}
	})
}

func TestEngineKeys(t *testing.T) {
	t.Run("Should give a KEY record that holds the user key", func(t *testing.T) {
		for _, user := range [][]byte{nil, []byte("a"), []byte("a\x00b"), bytes.Repeat([]byte{0xff}, 70000)} {
			k := keyOf(user)
			got, err := userKeyOf(k)
			if err != nil || !bytes.Equal(got, user) {
				t.Fatalf("got %q, %v", got, err)
			}
			if k[0] != classKey || len(k) != keyHeaderLen+len(user) {
				t.Fatalf("bad key % x", k[:min(len(k), 8)])
			}
		}
	})

	t.Run("Should refuse an engine key that is not a KEY record", func(t *testing.T) {
		for _, k := range [][]byte{nil, {}, {classKey}, {classKey, 1}, elementKey(1, 2, 0, nil), ownerKey(1, 2), systemKey("x")} {
			if _, err := userKeyOf(k); err == nil {
				t.Fatalf("no error for % x", k)
			}
		}
	})

	t.Run("Should keep the classes apart and in order", func(t *testing.T) {
		keys := [][]byte{systemKey("format"), keyOf([]byte("a")), elementKey(0, 1, 0, []byte("f")), ownerKey(0, 1)}
		for i := 1; i < len(keys); i++ {
			if bytes.Compare(keys[i-1], keys[i]) >= 0 {
				t.Fatalf("key %d is not above key %d", i, i-1)
			}
		}
		opts := classBounds(classKey)
		if bytes.Compare(keyOf([]byte("zzz")), opts.Upper) >= 0 || bytes.Compare(keyOf(nil), opts.Lower) < 0 {
			t.Fatal("a KEY record is outside the bounds of its class")
		}
		if bytes.Compare(elementKey(0, 1, 0, nil), opts.Upper) < 0 {
			t.Fatal("an ELEMENT record is inside the bounds of the KEY class")
		}
	})

	t.Run("Should keep the elements of a collection in a range of its own", func(t *testing.T) {
		in := elementBounds(7, 256)
		for _, uid := range []uint64{1, 255, 257, 1 << 40} {
			k := elementKey(7, uid, 0, []byte("field"))
			if bytes.Compare(k, in.Lower) >= 0 && bytes.Compare(k, in.Upper) < 0 {
				t.Fatalf("an element of %d is in the range of 256", uid)
			}
		}
		for _, field := range [][]byte{nil, []byte("a"), {0}, bytes.Repeat([]byte{0xff}, 100)} {
			k := elementKey(7, 256, 0, field)
			if bytes.Compare(k, in.Lower) < 0 || bytes.Compare(k, in.Upper) >= 0 {
				t.Fatalf("an element with field % x is outside its range", field)
			}
		}
		if bytes.Compare(elementKey(8, 256, 0, nil), in.Upper) < 0 && bytes.Compare(elementKey(8, 256, 0, nil), in.Lower) >= 0 {
			t.Fatal("an element of another slot is in the range")
		}
	})

	t.Run("Should not mix a field with the next uid", func(t *testing.T) {
		a := elementKey(1, 1, 0, []byte{0, 0, 0, 0, 0, 0, 0, 2})
		b := elementKey(1, 2, 0, nil)
		in := elementBounds(1, 1)
		if bytes.Compare(a, in.Upper) >= 0 || bytes.Compare(b, in.Upper) < 0 {
			t.Fatal("a field that looks like a uid crossed the end of its range")
		}
	})
}

func FuzzUserKeyOf(f *testing.F) {
	f.Add([]byte{})
	f.Add(keyOf([]byte("abc")))
	f.Add(elementKey(3, 4, 0, []byte("x")))
	f.Fuzz(func(t *testing.T, k []byte) {
		user, err := userKeyOf(k)
		if err != nil {
			return
		}
		if !bytes.Equal(engineKey(classKey, uint16(k[1])<<8|uint16(k[2]), user), k) {
			t.Fatalf("round trip changed % x", k)
		}
	})
}

func TestEnsureFormat(t *testing.T) {
	open := func(t *testing.T) *shale.DB {
		t.Helper()
		db, err := shale.Open(t.TempDir(), shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}

	t.Run("Should write the format of an empty store and accept it again", func(t *testing.T) {
		db := open(t)
		if err := EnsureFormat(db); err != nil {
			t.Fatal(err)
		}
		if err := EnsureFormat(db); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Should refuse a store that has keys and no format", func(t *testing.T) {
		db := open(t)
		if err := db.Put([]byte("old-key"), []byte("old-value")); err != nil {
			t.Fatal(err)
		}
		if err := EnsureFormat(db); err == nil {
			t.Fatal("a store with the old layout was accepted")
		}
		if _, err := db.Get(systemKey("format")); err == nil {
			t.Fatal("the format was written over a store with data")
		}
	})

	t.Run("Should refuse a store with another format", func(t *testing.T) {
		db := open(t)
		if err := db.Put(systemKey("format"), []byte{0, 0, 0, 9}); err != nil {
			t.Fatal(err)
		}
		if err := EnsureFormat(db); err == nil {
			t.Fatal("another format was accepted")
		}
		if err := db.Put(systemKey("format"), []byte{1}); err != nil {
			t.Fatal(err)
		}
		if err := EnsureFormat(db); err == nil {
			t.Fatal("a format record that is not valid was accepted")
		}
	})
}
