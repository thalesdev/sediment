// SPDX-License-Identifier: MIT

package command

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/thalesdev/sediment/resp"
)

func (te *typedEnv) members(key string) []string {
	te.t.Helper()
	v := te.run("SMEMBERS", key)
	out := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		out[i] = string(e.Bytes)
	}
	return out
}

func modelMembers(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestSetModel(t *testing.T) {
	t.Run("Should match a map for random commands, including the operations between sets", func(t *testing.T) {
		te := newTypedEnv(t)
		rng := rand.New(rand.NewSource(5))
		model := map[string]map[string]bool{}
		keys := []string{"s0", "s1", "s2", "s3"}
		for step := range 3000 {
			k := keys[rng.Intn(len(keys))]
			m := fmt.Sprintf("m%02d", rng.Intn(15))
			switch op := rng.Intn(12); {
			case op < 4:
				want := int64(1)
				if model[k][m] {
					want = 0
				}
				wantInteger(t, te.run("SADD", k, m), want)
				if model[k] == nil {
					model[k] = map[string]bool{}
				}
				model[k][m] = true
			case op < 6:
				want := int64(0)
				if model[k][m] {
					want = 1
				}
				wantInteger(t, te.run("SREM", k, m), want)
				delete(model[k], m)
				if len(model[k]) == 0 {
					delete(model, k)
				}
			case op == 6:
				wantInteger(t, te.run("DEL", k), map[bool]int64{true: 1, false: 0}[model[k] != nil])
				delete(model, k)
			case op == 7:
				te.run("SET", k, "plain")
				delete(model, k)
				te.run("DEL", k)
			case op == 8:
				dest := keys[rng.Intn(len(keys))]
				other := keys[rng.Intn(len(keys))]
				got := te.run("SUNIONSTORE", dest, k, other)
				union := map[string]bool{}
				for x := range model[k] {
					union[x] = true
				}
				for x := range model[other] {
					union[x] = true
				}
				wantInteger(t, got, int64(len(union)))
				if len(union) == 0 {
					delete(model, dest)
				} else {
					model[dest] = union
				}
			case op == 9:
				other := keys[rng.Intn(len(keys))]
				inter := map[string]bool{}
				for x := range model[k] {
					if model[other][x] {
						inter[x] = true
					}
				}
				if got := te.members(k); !slices.Equal(got, modelMembers(model[k])) {
					t.Fatalf("step %d: SMEMBERS %v, want %v", step, got, modelMembers(model[k]))
				}
				v := te.run("SINTER", k, other)
				var got []string
				for _, e := range v.Elems {
					got = append(got, string(e.Bytes))
				}
				if !slices.Equal(got, modelMembers(inter)) {
					t.Fatalf("step %d: SINTER %v, want %v", step, got, modelMembers(inter))
				}
			default:
				if got := te.members(k); !slices.Equal(got, modelMembers(model[k])) {
					t.Fatalf("step %d: SMEMBERS %v, want %v", step, got, modelMembers(model[k]))
				}
				wantInteger(t, te.run("SCARD", k), int64(len(model[k])))
			}
		}
		for _, k := range keys {
			wantInteger(t, te.run("SCARD", k), int64(len(model[k])))
		}
		te.sweep()
		live := map[uint64]bool{}
		for _, k := range keys {
			if rec, err := te.env.loadRecord([]byte(k)); err == nil && rec.typ == typeSet {
				uid, _ := rec.collection()
				live[uid] = true
			}
		}
		for uid := range te.allElements() {
			if !live[uid] {
				t.Fatalf("the elements of the dead set %d were not swept", uid)
			}
		}
	})

	t.Run("Should be a set kind for SMEMBERS", func(t *testing.T) {
		te := newTypedEnv(t)
		te.run("SADD", "s", "a")
		if got := te.run("SMEMBERS", "s"); got.Kind != resp.KindSet {
			t.Fatalf("kind %d", got.Kind)
		}
	})
}
