// SPDX-License-Identifier: MIT

package command

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
)

func (te *typedEnv) allElements() map[uint64]int {
	te.t.Helper()
	it, err := te.db.NewIterator(classBounds(classElement))
	if err != nil {
		te.t.Fatal(err)
	}
	defer it.Close()
	out := map[uint64]int{}
	for it.SeekToFirst(); it.Valid(); it.Next() {
		k := it.Key()
		var uid uint64
		for _, b := range k[keyHeaderLen : keyHeaderLen+8] {
			uid = uid<<8 | uint64(b)
		}
		out[uid]++
	}
	return out
}

func (te *typedEnv) hashFields(key string) map[string]string {
	te.t.Helper()
	v := te.run("HGETALL", key)
	out := map[string]string{}
	for i := 0; i+1 < len(v.Elems); i += 2 {
		out[string(v.Elems[i].Bytes)] = string(v.Elems[i+1].Bytes)
	}
	return out
}

func TestHashModel(t *testing.T) {
	t.Run("Should match a map for random commands and keep HLEN equal to the fields", func(t *testing.T) {
		te := newTypedEnv(t)
		rng := rand.New(rand.NewSource(3))
		model := map[string]map[string]string{}
		strings := map[string]bool{}
		keys := []string{"h0", "h1", "h2", "h3"}
		for step := range 3000 {
			k := keys[rng.Intn(len(keys))]
			f := fmt.Sprintf("f%02d", rng.Intn(12))
			switch op := rng.Intn(10); {
			case op < 4:
				v := fmt.Sprint(step)
				if strings[k] {
					if got := te.run("HSET", k, f, v); got.Kind != resp.KindError {
						t.Fatalf("HSET on a string gave %+v", got)
					}
					continue
				}
				_, existed := model[k][f]
				want := int64(1)
				if existed {
					want = 0
				}
				wantInteger(t, te.run("HSET", k, f, v), want)
				if model[k] == nil {
					model[k] = map[string]string{}
				}
				model[k][f] = v
			case op < 6:
				if strings[k] {
					continue
				}
				_, existed := model[k][f]
				want := int64(0)
				if existed {
					want = 1
				}
				wantInteger(t, te.run("HDEL", k, f), want)
				delete(model[k], f)
				if len(model[k]) == 0 {
					delete(model, k)
				}
			case op == 6:
				wantInteger(t, te.run("DEL", k), map[bool]int64{true: 1, false: 0}[model[k] != nil || strings[k]])
				delete(model, k)
				delete(strings, k)
			case op == 7:
				te.run("SET", k, "plain")
				delete(model, k)
				strings[k] = true
			case op == 8 && !strings[k] && model[k] != nil:
				wantInteger(t, te.run("EXPIRE", k, "100"), 1)
			default:
				if strings[k] {
					continue
				}
				got := te.hashFields(k)
				want := model[k]
				if want == nil {
					want = map[string]string{}
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("step %d: HGETALL %v, want %v", step, got, want)
				}
				wantInteger(t, te.run("HLEN", k), int64(len(want)))
			}
		}
		for _, k := range keys {
			if strings[k] {
				continue
			}
			want := model[k]
			wantInteger(t, te.run("HLEN", k), int64(len(want)))
			if len(te.hashFields(k)) != len(want) {
				t.Fatalf("%s: fields differ", k)
			}
		}
		te.sweep()
		live := map[uint64]bool{}
		for _, k := range keys {
			if rec, err := te.env.loadRecord([]byte(k)); err == nil && rec.typ == typeHash {
				uid, _ := rec.collection()
				live[uid] = true
			}
		}
		for uid := range te.allElements() {
			if !live[uid] {
				t.Fatalf("the elements of the dead collection %d were not swept", uid)
			}
		}
	})
}

func (te *typedEnv) sweep() int {
	te.t.Helper()
	n, err := te.env.Sweep()
	if err != nil {
		te.t.Fatal(err)
	}
	return n
}

func TestSweep(t *testing.T) {
	t.Run("Should remove the elements that a plain SET left behind", func(t *testing.T) {
		te := newTypedEnv(t)
		te.run("HSET", "h", "a", "1", "b", "2")
		rec, _ := te.env.loadRecord([]byte("h"))
		uid, _ := rec.collection()
		te.env.save([]byte("h"), []byte("plain"), 0)
		if te.elements(slotOf([]byte("h")), uid) != 2 {
			t.Fatal("setup")
		}
		if n := te.sweep(); n != 1 {
			t.Fatalf("swept %d collections", n)
		}
		if te.elements(slotOf([]byte("h")), uid) != 0 || te.has(ownerKey(slotOf([]byte("h")), uid)) {
			t.Fatal("the elements or the OWNER record are left")
		}
		wantReply(t, te.run("GET", "h"), resp.KindBulk, "plain")
		if n := te.sweep(); n != 0 {
			t.Fatalf("a second pass swept %d", n)
		}
	})

	t.Run("Should remove the elements of a hash that expired", func(t *testing.T) {
		te := newTypedEnv(t)
		te.run("HSET", "h", "a", "1")
		te.run("EXPIRE", "h", "5")
		if n := te.sweep(); n != 0 {
			t.Fatalf("swept %d live collections", n)
		}
		te.clock += 6000
		if n := te.sweep(); n != 1 {
			t.Fatalf("swept %d", n)
		}
		if len(te.allElements()) != 0 {
			t.Fatal("elements are left")
		}
	})

	t.Run("Should keep a live collection and one that was made again", func(t *testing.T) {
		te := newTypedEnv(t)
		te.run("HSET", "keep", "a", "1")
		te.run("HSET", "again", "a", "1")
		te.run("DEL", "again")
		te.run("HSET", "again", "b", "2")
		te.sweep()
		wantReply(t, te.run("HGET", "keep", "a"), resp.KindBulk, "1")
		wantReply(t, te.run("HGET", "again", "b"), resp.KindBulk, "2")
		if len(te.allElements()) != 2 {
			t.Fatalf("%v", te.allElements())
		}
	})

	t.Run("Should sweep in more than one write", func(t *testing.T) {
		te := newTypedEnv(t)
		for i := range sweepBatch + 40 {
			k := fmt.Sprintf("h%d", i)
			te.run("HSET", k, "a", "1")
			te.env.save([]byte(k), []byte("x"), 0)
		}
		if n := te.sweep(); n != sweepBatch+40 {
			t.Fatalf("swept %d", n)
		}
		if len(te.allElements()) != 0 {
			t.Fatal("elements are left")
		}
	})

	t.Run("Should stop the sweeper when asked", func(t *testing.T) {
		te := newTypedEnv(t)
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() { te.env.RunSweeper(stop, time.Hour); close(done) }()
		close(stop)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the sweeper did not stop")
		}
	})
}

func TestAllocUID(t *testing.T) {
	t.Run("Should give ids that go up and are not used twice after a restart", func(t *testing.T) {
		dir := t.TempDir()
		db, err := shale.Open(dir, shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		env := &Env{Store: db, Locks: NewLocks()}
		var last uint64
		for range 10 {
			uid, err := env.allocUID()
			if err != nil {
				t.Fatal(err)
			}
			if uid <= last {
				t.Fatalf("uid %d after %d", uid, last)
			}
			last = uid
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db, err = shale.Open(dir, shale.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		env = &Env{Store: db, Locks: NewLocks()}
		uid, err := env.allocUID()
		if err != nil {
			t.Fatal(err)
		}
		if uid <= last {
			t.Fatalf("uid %d after a restart, %d was used before", uid, last)
		}
	})

	t.Run("Should write the high mark one time for a block of ids", func(t *testing.T) {
		te := newTypedEnv(t)
		for range uidBlock {
			if _, err := te.env.allocUID(); err != nil {
				t.Fatal(err)
			}
		}
		uid, err := te.env.allocUID()
		if err != nil || uid != uidBlock+1 {
			t.Fatalf("uid %d, %v", uid, err)
		}
	})
}

func TestRenameKeepsNoOrphans(t *testing.T) {
	for _, pair := range [][2]string{{"{t}a", "{t}b"}, {"alpha", "omega"}} {
		t.Run("Should leave no element behind after RENAME of "+pair[0], func(t *testing.T) {
			te := newTypedEnv(t)
			te.run("HSET", pair[0], "a", "1", "b", "2", "c", "3")
			te.run("HSET", pair[1], "x", "1", "y", "2")
			wantReply(t, te.run("RENAME", pair[0], pair[1]), resp.KindSimple, "OK")
			if n := te.sweep(); n != 0 {
				t.Fatalf("the sweeper found %d dead collections after RENAME, so it left orphans", n)
			}
			rec, err := te.env.loadRecord([]byte(pair[1]))
			if err != nil {
				t.Fatal(err)
			}
			uid, _ := rec.collection()
			all := te.allElements()
			if len(all) != 1 || all[uid] != 3 {
				t.Fatalf("elements by collection: %v, want 3 under %d", all, uid)
			}
		})
	}
}
