// SPDX-License-Identifier: MIT

package command

import (
	"fmt"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
)

type typedEnv struct {
	t     *testing.T
	env   *Env
	db    *shale.DB
	reg   *Registry
	clock int64
}

func newTypedEnv(t *testing.T) *typedEnv {
	t.Helper()
	db, err := shale.Open(t.TempDir(), shale.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	te := &typedEnv{t: t, db: db, reg: NewRegistry(), clock: 1_700_000_000_000}
	te.env = &Env{Store: db, Locks: NewLocks(), MaxReply: 1 << 20, Now: func() time.Time { return time.UnixMilli(te.clock) }}
	return te
}

func (te *typedEnv) run(args ...string) resp.Value {
	te.t.Helper()
	raw := make([][]byte, len(args))
	for i, a := range args {
		raw[i] = []byte(a)
	}
	spec := te.reg.Lookup(raw[0])
	if spec == nil {
		te.t.Fatalf("no command %s", args[0])
	}
	return spec.Handler(te.env, &Client{}, raw)
}

func (te *typedEnv) seed(key string, typ valueType, uid uint64, deadline int64, fields ...string) {
	te.t.Helper()
	slot := slotOf([]byte(key))
	var b shale.Batch
	b.Put(keyOf([]byte(key)), encodeRecord(typ, deadline, collectionBody(uid, uint64(len(fields)))))
	b.Put(ownerKey(slot, uid), []byte(key))
	for _, f := range fields {
		b.Put(elementKey(slot, uid, 0, []byte(f)), []byte{0, 'v'})
	}
	if err := te.db.Write(&b); err != nil {
		te.t.Fatal(err)
	}
}

func (te *typedEnv) elements(slot uint16, uid uint64) int {
	te.t.Helper()
	it, err := te.db.NewIterator(elementBounds(slot, uid))
	if err != nil {
		te.t.Fatal(err)
	}
	defer it.Close()
	n := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		n++
	}
	return n
}

func (te *typedEnv) has(key []byte) bool {
	te.t.Helper()
	_, err := te.db.Get(key)
	return err == nil
}

func wantReply(t *testing.T, got resp.Value, kind resp.Kind, text string) {
	t.Helper()
	if got.Kind != kind || string(got.Bytes) != text {
		t.Fatalf("got kind %d %q, want kind %d %q", got.Kind, got.Bytes, kind, text)
	}
}

func wantInteger(t *testing.T, got resp.Value, n int64) {
	t.Helper()
	if got.Kind != resp.KindInt || got.Int != n {
		t.Fatalf("got kind %d %d %q, want %d", got.Kind, got.Int, got.Bytes, n)
	}
}

func TestTypedKeys(t *testing.T) {
	t.Run("Should report the type of a collection", func(t *testing.T) {
		te := newTypedEnv(t)
		te.seed("h", typeHash, 1, 0, "a")
		te.seed("s", typeSet, 2, 0, "a")
		te.run("SET", "str", "v")
		wantReply(t, te.run("TYPE", "h"), resp.KindSimple, "hash")
		wantReply(t, te.run("TYPE", "s"), resp.KindSimple, "set")
		wantReply(t, te.run("TYPE", "str"), resp.KindSimple, "string")
		wantReply(t, te.run("TYPE", "none"), resp.KindSimple, "none")
	})

	t.Run("Should refuse a string command on a collection", func(t *testing.T) {
		te := newTypedEnv(t)
		te.seed("h", typeHash, 1, 0, "a")
		for _, args := range [][]string{{"GET", "h"}, {"STRLEN", "h"}, {"INCR", "h"}, {"APPEND", "h", "x"}, {"GETSET", "h", "x"}, {"GETDEL", "h"}} {
			got := te.run(args...)
			if got.Kind != resp.KindError || string(got.Bytes[:9]) != "WRONGTYPE" {
				t.Fatalf("%v gave kind %d %q", args, got.Kind, got.Bytes)
			}
		}
		wantReply(t, te.run("TYPE", "h"), resp.KindSimple, "hash")
	})

	t.Run("Should give null for a collection in MGET and count it in EXISTS", func(t *testing.T) {
		te := newTypedEnv(t)
		te.seed("h", typeHash, 1, 0, "a")
		te.run("SET", "str", "v")
		got := te.run("MGET", "h", "str", "none")
		if got.Kind != resp.KindArray || len(got.Elems) != 3 || got.Elems[0].Kind != resp.KindNull || string(got.Elems[1].Bytes) != "v" || got.Elems[2].Kind != resp.KindNull {
			t.Fatalf("got %+v", got)
		}
		wantInteger(t, te.run("EXISTS", "h", "str", "none"), 2)
		wantInteger(t, te.run("SETNX", "h", "x"), 0)
	})

	t.Run("Should delete the elements of a collection with DEL in one write", func(t *testing.T) {
		te := newTypedEnv(t)
		slot := slotOf([]byte("h"))
		var fields []string
		for i := range 500 {
			fields = append(fields, fmt.Sprintf("field-%04d", i))
		}
		te.seed("h", typeHash, 7, 0, fields...)
		te.seed("other", typeHash, 8, 0, "x", "y")
		if te.elements(slot, 7) != 500 {
			t.Fatal("setup")
		}
		wantInteger(t, te.run("DEL", "h"), 1)
		wantReply(t, te.run("TYPE", "h"), resp.KindSimple, "none")
		if n := te.elements(slot, 7); n != 0 {
			t.Fatalf("%d elements are left", n)
		}
		if te.has(ownerKey(slot, 7)) || te.has(keyOf([]byte("h"))) {
			t.Fatal("the KEY or the OWNER record is left")
		}
		if te.elements(slotOf([]byte("other")), 8) != 2 {
			t.Fatal("DEL touched another collection")
		}
	})

	t.Run("Should replace a collection with SET and remove its elements when SET reads the key", func(t *testing.T) {
		te := newTypedEnv(t)
		slot := slotOf([]byte("h"))
		te.seed("h", typeHash, 3, 0, "a", "b")
		wantReply(t, te.run("SET", "h", "v", "XX"), resp.KindSimple, "OK")
		wantReply(t, te.run("GET", "h"), resp.KindBulk, "v")
		if n := te.elements(slot, 3); n != 0 {
			t.Fatalf("%d elements are left", n)
		}
		if te.has(ownerKey(slot, 3)) {
			t.Fatal("the OWNER record is left")
		}
	})

	t.Run("Should not overwrite a collection with SET NX or GET", func(t *testing.T) {
		te := newTypedEnv(t)
		te.seed("h", typeHash, 3, 0, "a")
		if got := te.run("SET", "h", "v", "NX"); got.Kind != resp.KindNull {
			t.Fatalf("SET NX gave %+v", got)
		}
		if got := te.run("SET", "h", "v", "GET"); got.Kind != resp.KindError {
			t.Fatalf("SET GET gave %+v", got)
		}
		wantReply(t, te.run("TYPE", "h"), resp.KindSimple, "hash")
	})

	t.Run("Should expire a collection and keep its type and elements until then", func(t *testing.T) {
		te := newTypedEnv(t)
		te.seed("h", typeHash, 4, 0, "a")
		wantInteger(t, te.run("EXPIRE", "h", "10"), 1)
		wantInteger(t, te.run("TTL", "h"), 10)
		wantReply(t, te.run("TYPE", "h"), resp.KindSimple, "hash")
		wantInteger(t, te.run("PERSIST", "h"), 1)
		wantInteger(t, te.run("TTL", "h"), -1)
		wantInteger(t, te.run("EXPIRE", "h", "10"), 1)
		te.clock += 11_000
		wantReply(t, te.run("TYPE", "h"), resp.KindSimple, "none")
		wantInteger(t, te.run("EXISTS", "h"), 0)
	})

	t.Run("Should delete a collection when EXPIRE gets a time in the past", func(t *testing.T) {
		te := newTypedEnv(t)
		te.seed("h", typeHash, 5, 0, "a", "b")
		wantInteger(t, te.run("EXPIRE", "h", "-1"), 1)
		if n := te.elements(slotOf([]byte("h")), 5); n != 0 {
			t.Fatalf("%d elements are left", n)
		}
	})

	t.Run("Should show one key for a collection in KEYS, SCAN, DBSIZE and RANDOMKEY", func(t *testing.T) {
		te := newTypedEnv(t)
		te.seed("h", typeHash, 6, 0, "a", "b", "c")
		te.run("SET", "str", "v")
		wantInteger(t, te.run("DBSIZE"), 2)
		keys := te.run("KEYS", "*")
		if keys.Kind != resp.KindArray || len(keys.Elems) != 2 {
			t.Fatalf("KEYS gave %+v", keys)
		}
		page := te.run("SCAN", "0", "COUNT", "100", "TYPE", "hash")
		if len(page.Elems) != 2 || len(page.Elems[1].Elems) != 1 || string(page.Elems[1].Elems[0].Bytes) != "h" {
			t.Fatalf("SCAN TYPE hash gave %+v", page)
		}
		for range 30 {
			got := te.run("RANDOMKEY")
			if got.Kind != resp.KindBulk || (string(got.Bytes) != "h" && string(got.Bytes) != "str") {
				t.Fatalf("RANDOMKEY gave %+v", got)
			}
		}
	})
}
