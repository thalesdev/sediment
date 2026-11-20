// SPDX-License-Identifier: MIT

package command

import (
	"testing"

	"github.com/thalesdev/sediment/internal/shale"
)

func TestKeysOf(t *testing.T) {
	args := func(s ...string) [][]byte {
		out := make([][]byte, len(s))
		for i, x := range s {
			out[i] = []byte(x)
		}
		return out
	}
	join := func(keys [][]byte) string {
		var out string
		for _, k := range keys {
			out += string(k) + ","
		}
		return out
	}
	reg := NewRegistry()
	for _, c := range []struct {
		cmd  []string
		want string
	}{
		{[]string{"GET", "a"}, "a,"},
		{[]string{"MSET", "a", "1", "b", "2"}, "a,b,"},
		{[]string{"DEL", "a", "b", "c"}, "a,b,c,"},
		{[]string{"SUNIONSTORE", "d", "a", "b"}, "d,a,b,"},
		{[]string{"MGET", "a", "b"}, "a,b,"},
		{[]string{"PING"}, ""},
		{[]string{"SCAN", "0"}, ""},
		{[]string{"MSET", "a", "1", "b"}, "a,b,"},
	} {
		t.Run("Should find the keys of "+c.cmd[0], func(t *testing.T) {
			spec := reg.Lookup([]byte(c.cmd[0]))
			if got := join(KeysOf(spec, args(c.cmd...))); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestVersions(t *testing.T) {
	t.Run("Should count the writes of a KEY record and nothing else", func(t *testing.T) {
		db, err := shale.Open(t.TempDir(), shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		v := NewVersions()
		store := WithVersions(db, v)
		k := []byte("key")
		if v.Load(k) != 0 {
			t.Fatal("a new count is not zero")
		}
		if err := store.Put(keyOf(k), encodeRecord(typeString, 0, []byte("v"))); err != nil {
			t.Fatal(err)
		}
		if v.Load(k) != 1 {
			t.Fatalf("count %d after a Put", v.Load(k))
		}
		if err := store.Put(elementKey(slotOf(k), 1, 0, []byte("f")), elementValue(nil)); err != nil {
			t.Fatal(err)
		}
		if err := store.Put(ownerKey(slotOf(k), 1), k); err != nil {
			t.Fatal(err)
		}
		if v.Load(k) != 1 {
			t.Fatalf("count %d after the writes of an element and an OWNER record", v.Load(k))
		}
		var b shale.Batch
		b.Put(keyOf(k), encodeRecord(typeString, 0, []byte("w")))
		b.Delete(keyOf(k))
		b.DeleteRange([]byte{classElement}, []byte{classElement + 1})
		if err := store.Write(&b); err != nil {
			t.Fatal(err)
		}
		if v.Load(k) != 3 {
			t.Fatalf("count %d after a batch of two KEY writes and a range delete", v.Load(k))
		}
		if err := store.Delete(keyOf(k)); err != nil {
			t.Fatal(err)
		}
		if v.Load(k) != 4 {
			t.Fatalf("count %d after a Delete", v.Load(k))
		}
	})

	t.Run("Should count nothing for a nil Versions", func(t *testing.T) {
		var v *Versions
		if v.Load([]byte("k")) != 0 {
			t.Fatal("a nil count is not zero")
		}
	})
}
