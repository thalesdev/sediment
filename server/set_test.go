// SPDX-License-Identifier: MIT

package server

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/thalesdev/sediment/resp"
)

func sortedStrs(t *testing.T, v resp.Value) []string {
	t.Helper()
	out := strs(t, v)
	slices.Sort(out)
	return out
}

func TestSets(t *testing.T) {
	clock := &fakeClock{}
	clock.ms.Store(1_700_000_000_000)
	_, addr := startServer(t, Options{Now: clock.now, SweepInterval: -1})
	c := dial(t, addr)

	t.Run("Should add members and count the new ones", func(t *testing.T) {
		wantInt(t, c.do("SADD", "s", "a", "b", "c"), 3)
		wantInt(t, c.do("SADD", "s", "c", "d", "d"), 1)
		wantInt(t, c.do("SCARD", "s"), 4)
		wantInt(t, c.do("SCARD", "missing"), 0)
		wantInt(t, c.do("SISMEMBER", "s", "a"), 1)
		wantInt(t, c.do("SISMEMBER", "s", "zz"), 0)
		wantInt(t, c.do("SISMEMBER", "missing", "a"), 0)
		wantSimple(t, c.do("TYPE", "s"), "set")
	})

	t.Run("Should answer SMISMEMBER with one number for each member", func(t *testing.T) {
		v := c.do("SMISMEMBER", "s", "a", "zz", "d")
		if v.Kind != resp.KindArray || len(v.Elems) != 3 || v.Elems[0].Int != 1 || v.Elems[1].Int != 0 || v.Elems[2].Int != 1 {
			t.Fatalf("got %+v", v)
		}
	})

	t.Run("Should give the members in order with SMEMBERS", func(t *testing.T) {
		wantStrs(t, c.do("SMEMBERS", "s"), "a", "b", "c", "d")
		wantStrs(t, c.do("SMEMBERS", "missing"))
	})

	t.Run("Should give a set with SMEMBERS in RESP3", func(t *testing.T) {
		d := dial(t, addr)
		d.do("HELLO", "3")
		v := d.do("SMEMBERS", "s")
		if v.Kind != resp.KindSet {
			t.Fatalf("kind %d, want a set", v.Kind)
		}
		wantStrs(t, v, "a", "b", "c", "d")
	})

	t.Run("Should remove members and drop the set when it is empty", func(t *testing.T) {
		c.do("SADD", "r", "a", "b", "c")
		wantInt(t, c.do("SREM", "r", "a", "zz", "a"), 1)
		wantInt(t, c.do("SCARD", "r"), 2)
		wantInt(t, c.do("SREM", "missing", "a"), 0)
		wantInt(t, c.do("SREM", "r", "b", "c"), 2)
		wantSimple(t, c.do("TYPE", "r"), "none")
		wantInt(t, c.do("SADD", "r", "x"), 1)
		wantStrs(t, c.do("SMEMBERS", "r"), "x")
	})

	t.Run("Should compute the union, the intersection and the difference", func(t *testing.T) {
		c.do("SADD", "u1", "a", "b", "c", "d")
		c.do("SADD", "u2", "c", "d", "e")
		c.do("SADD", "u3", "d", "e", "f")
		wantStrs(t, c.do("SUNION", "u1", "u2", "u3"), "a", "b", "c", "d", "e", "f")
		wantStrs(t, c.do("SINTER", "u1", "u2", "u3"), "d")
		wantStrs(t, c.do("SINTER", "u1", "u2"), "c", "d")
		wantStrs(t, c.do("SDIFF", "u1", "u2", "u3"), "a", "b")
		wantStrs(t, c.do("SDIFF", "u2", "u1"), "e")
		wantStrs(t, c.do("SINTER", "u1", "missing"))
		wantStrs(t, c.do("SUNION", "u1", "missing"), "a", "b", "c", "d")
		wantStrs(t, c.do("SDIFF", "missing", "u1"))
		wantStrs(t, c.do("SINTER", "u1"), "a", "b", "c", "d")
	})

	t.Run("Should store the result and replace the destination", func(t *testing.T) {
		wantInt(t, c.do("SUNIONSTORE", "dest", "u1", "u2"), 5)
		wantStrs(t, c.do("SMEMBERS", "dest"), "a", "b", "c", "d", "e")
		wantInt(t, c.do("SINTERSTORE", "dest", "u1", "u3"), 1)
		wantStrs(t, c.do("SMEMBERS", "dest"), "d")
		wantInt(t, c.do("SDIFFSTORE", "dest", "u1", "u1"), 0)
		wantSimple(t, c.do("TYPE", "dest"), "none")
		c.do("SET", "str-dest", "v")
		wantInt(t, c.do("SUNIONSTORE", "str-dest", "u1"), 4)
		wantSimple(t, c.do("TYPE", "str-dest"), "set")
		wantInt(t, c.do("SUNIONSTORE", "u1", "u1", "u2"), 5)
		wantStrs(t, c.do("SMEMBERS", "u1"), "a", "b", "c", "d", "e")
	})

	t.Run("Should pop members with SPOP", func(t *testing.T) {
		c.do("SADD", "p", "a", "b", "c", "d", "e")
		got := string(c.do("SPOP", "p").Bytes)
		if got == "" || len(got) != 1 {
			t.Fatalf("SPOP gave %q", got)
		}
		wantInt(t, c.do("SCARD", "p"), 4)
		wantInt(t, c.do("SISMEMBER", "p", got), 0)
		many := strs(t, c.do("SPOP", "p", "3"))
		if len(many) != 3 {
			t.Fatalf("SPOP 3 gave %v", many)
		}
		wantInt(t, c.do("SCARD", "p"), 1)
		rest := strs(t, c.do("SPOP", "p", "10"))
		if len(rest) != 1 {
			t.Fatalf("SPOP 10 gave %v", rest)
		}
		wantSimple(t, c.do("TYPE", "p"), "none")
		wantNull(t, c.do("SPOP", "p"))
		wantStrs(t, c.do("SPOP", "p", "2"))
		wantError(t, c.do("SPOP", "s", "-1"), "ERR value is out of range")
		wantError(t, c.do("SPOP", "s", "x"), "ERR value is not an integer")
	})

	t.Run("Should give random members with SRANDMEMBER", func(t *testing.T) {
		c.do("SADD", "q", "a", "b", "c", "d", "e")
		one := string(c.do("SRANDMEMBER", "q").Bytes)
		if len(one) != 1 {
			t.Fatalf("got %q", one)
		}
		three := strs(t, c.do("SRANDMEMBER", "q", "3"))
		slices.Sort(three)
		if len(three) != 3 || len(slices.Compact(slices.Clone(three))) != 3 {
			t.Fatalf("got %v", three)
		}
		if got := strs(t, c.do("SRANDMEMBER", "q", "50")); len(got) != 5 {
			t.Fatalf("a count above the size gave %v", got)
		}
		if got := strs(t, c.do("SRANDMEMBER", "q", "-12")); len(got) != 12 {
			t.Fatalf("a negative count gave %v", got)
		}
		wantInt(t, c.do("SCARD", "q"), 5)
		wantNull(t, c.do("SRANDMEMBER", "missing"))
		wantStrs(t, c.do("SRANDMEMBER", "missing", "3"))
	})

	t.Run("Should scan the members with SSCAN", func(t *testing.T) {
		for i := range 37 {
			c.do("SADD", "big", fmt.Sprintf("m%03d", i))
		}
		seen := map[string]bool{}
		cursor := "0"
		for range 100 {
			v := c.do("SSCAN", "big", cursor, "COUNT", "6")
			next := string(v.Elems[0].Bytes)
			for _, m := range strs(t, v.Elems[1]) {
				seen[m] = true
			}
			if next == "0" {
				break
			}
			cursor = next
		}
		if len(seen) != 37 {
			t.Fatalf("SSCAN saw %d members", len(seen))
		}
		v := c.do("SSCAN", "big", "0", "MATCH", "m00?", "COUNT", "100")
		if got := strs(t, v.Elems[1]); len(got) != 10 {
			t.Fatalf("MATCH gave %v", got)
		}
	})

	t.Run("Should refuse the wrong type", func(t *testing.T) {
		c.do("SET", "str", "v")
		c.do("HSET", "hash", "a", "1")
		for _, args := range [][]string{
			{"SADD", "str", "a"}, {"SADD", "hash", "a"}, {"SCARD", "str"}, {"SMEMBERS", "hash"}, {"SREM", "hash", "a"},
			{"SUNION", "s", "str"}, {"SINTER", "s", "hash"}, {"SDIFF", "hash", "s"}, {"SUNIONSTORE", "d", "str"},
			{"SPOP", "str"}, {"SRANDMEMBER", "hash"}, {"SISMEMBER", "hash", "a"},
		} {
			wantError(t, c.do(args...), "WRONGTYPE")
		}
		wantError(t, c.do("HGET", "s", "a"), "WRONGTYPE")
		wantError(t, c.do("GET", "s"), "WRONGTYPE")
	})

	t.Run("Should expire a set and let it start again", func(t *testing.T) {
		c.do("SADD", "e", "a", "b")
		wantInt(t, c.do("EXPIRE", "e", "5"), 1)
		wantInt(t, c.do("SADD", "e", "c"), 1)
		wantInt(t, c.do("TTL", "e"), 5)
		clock.advance(6 * time.Second)
		wantInt(t, c.do("SCARD", "e"), 0)
		wantStrs(t, c.do("SMEMBERS", "e"))
		wantInt(t, c.do("SADD", "e", "fresh"), 1)
		wantInt(t, c.do("TTL", "e"), -1)
		wantStrs(t, c.do("SMEMBERS", "e"), "fresh")
	})

	t.Run("Should count a set as one key", func(t *testing.T) {
		d := dial(t, addr)
		wantStrs(t, d.do("KEYS", "big"), "big")
		if got := scanAll(t, d, "TYPE", "set", "MATCH", "big"); len(got) != 1 {
			t.Fatalf("SCAN gave %v", got)
		}
	})

	t.Run("Should check the number of arguments", func(t *testing.T) {
		wantError(t, c.do("SADD", "s"), "ERR wrong number")
		wantError(t, c.do("SUNIONSTORE", "d"), "ERR wrong number")
		wantError(t, c.do("SPOP"), "ERR wrong number")
	})
}
