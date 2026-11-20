// SPDX-License-Identifier: MIT

package server

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/thalesdev/sediment/resp"
)

func strs(t *testing.T, v resp.Value) []string {
	t.Helper()
	if v.Kind != resp.KindArray && v.Kind != resp.KindMap && v.Kind != resp.KindSet {
		t.Fatalf("not a list: kind %d %q", v.Kind, v.Bytes)
	}
	out := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		if e.Kind == resp.KindNull {
			out[i] = "<nil>"
			continue
		}
		out[i] = string(e.Bytes)
	}
	return out
}

func wantStrs(t *testing.T, v resp.Value, want ...string) {
	t.Helper()
	if got := strs(t, v); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHashes(t *testing.T) {
	clock := &fakeClock{}
	clock.ms.Store(1_700_000_000_000)
	_, addr := startServer(t, Options{Now: clock.now, SweepInterval: -1})
	c := dial(t, addr)

	t.Run("Should set and get fields and count the new ones", func(t *testing.T) {
		wantInt(t, c.do("HSET", "h", "a", "1", "b", "2"), 2)
		wantInt(t, c.do("HSET", "h", "b", "3", "c", "4"), 1)
		wantBulk(t, c.do("HGET", "h", "a"), "1")
		wantBulk(t, c.do("HGET", "h", "b"), "3")
		wantNull(t, c.do("HGET", "h", "zz"))
		wantNull(t, c.do("HGET", "missing", "a"))
		wantInt(t, c.do("HLEN", "h"), 3)
		wantInt(t, c.do("HLEN", "missing"), 0)
		wantInt(t, c.do("HEXISTS", "h", "c"), 1)
		wantInt(t, c.do("HEXISTS", "h", "zz"), 0)
		wantInt(t, c.do("HSTRLEN", "h", "b"), 1)
		wantInt(t, c.do("HSTRLEN", "h", "zz"), 0)
		wantSimple(t, c.do("TYPE", "h"), "hash")
	})

	t.Run("Should count a field once when it repeats in one command", func(t *testing.T) {
		wantInt(t, c.do("HSET", "rep", "f", "1", "f", "2", "g", "3"), 2)
		wantBulk(t, c.do("HGET", "rep", "f"), "2")
		wantInt(t, c.do("HLEN", "rep"), 2)
	})

	t.Run("Should HMSET and HMGET", func(t *testing.T) {
		wantSimple(t, c.do("HMSET", "m", "a", "1", "b", "2"), "OK")
		wantStrs(t, c.do("HMGET", "m", "a", "zz", "b"), "1", "<nil>", "2")
		wantStrs(t, c.do("HMGET", "nope", "a", "b"), "<nil>", "<nil>")
		wantError(t, c.do("HMSET", "m", "a"), "ERR wrong number")
		wantError(t, c.do("HSET", "m", "a", "1", "b"), "ERR wrong number")
	})

	t.Run("Should HSETNX only for a field that does not exist", func(t *testing.T) {
		wantInt(t, c.do("HSETNX", "nx", "a", "1"), 1)
		wantInt(t, c.do("HSETNX", "nx", "a", "2"), 0)
		wantInt(t, c.do("HSETNX", "nx", "b", "3"), 1)
		wantBulk(t, c.do("HGET", "nx", "a"), "1")
		wantInt(t, c.do("HLEN", "nx"), 2)
	})

	t.Run("Should HDEL fields and drop the hash when it is empty", func(t *testing.T) {
		c.do("HSET", "d", "a", "1", "b", "2", "c", "3")
		wantInt(t, c.do("HDEL", "d", "a", "zz", "a"), 1)
		wantInt(t, c.do("HLEN", "d"), 2)
		wantInt(t, c.do("HDEL", "d", "zz"), 0)
		wantInt(t, c.do("HDEL", "missing", "a"), 0)
		wantInt(t, c.do("HDEL", "d", "b", "c"), 2)
		wantSimple(t, c.do("TYPE", "d"), "none")
		wantInt(t, c.do("EXISTS", "d"), 0)
		wantInt(t, c.do("HSET", "d", "again", "1"), 1)
		wantInt(t, c.do("HLEN", "d"), 1)
		wantNull(t, c.do("HGET", "d", "a"))
	})

	t.Run("Should HINCRBY and HINCRBYFLOAT", func(t *testing.T) {
		wantInt(t, c.do("HINCRBY", "i", "n", "5"), 5)
		wantInt(t, c.do("HINCRBY", "i", "n", "-7"), -2)
		wantInt(t, c.do("HLEN", "i"), 1)
		c.do("HSET", "i", "s", "abc")
		wantError(t, c.do("HINCRBY", "i", "s", "1"), "ERR hash value is not an integer")
		wantError(t, c.do("HINCRBY", "i", "n", "x"), "ERR value is not an integer")
		c.do("HSET", "i", "max", "9223372036854775807")
		wantError(t, c.do("HINCRBY", "i", "max", "1"), "ERR increment or decrement would overflow")
		wantBulk(t, c.do("HINCRBYFLOAT", "i", "f", "1.5"), "1.5")
		wantBulk(t, c.do("HINCRBYFLOAT", "i", "f", "0.25"), "1.75")
		wantError(t, c.do("HINCRBYFLOAT", "i", "s", "1"), "ERR hash value is not a float")
		wantError(t, c.do("HINCRBYFLOAT", "i", "f", "nope"), "ERR value is not a valid float")
	})

	t.Run("Should give all the fields in order with HGETALL, HKEYS and HVALS", func(t *testing.T) {
		c.do("HSET", "all", "b", "2", "a", "1", "c", "3")
		wantStrs(t, c.do("HGETALL", "all"), "a", "1", "b", "2", "c", "3")
		wantStrs(t, c.do("HKEYS", "all"), "a", "b", "c")
		wantStrs(t, c.do("HVALS", "all"), "1", "2", "3")
		wantStrs(t, c.do("HGETALL", "missing"))
	})

	t.Run("Should give a map with HGETALL in RESP3", func(t *testing.T) {
		d := dial(t, addr)
		d.do("HELLO", "3")
		v := d.do("HGETALL", "all")
		if v.Kind != resp.KindMap {
			t.Fatalf("kind %d, want a map", v.Kind)
		}
		wantStrs(t, v, "a", "1", "b", "2", "c", "3")
	})

	t.Run("Should refuse the wrong type in both directions", func(t *testing.T) {
		c.do("SET", "str", "v")
		wantError(t, c.do("HSET", "str", "a", "1"), "WRONGTYPE")
		wantError(t, c.do("HGET", "str", "a"), "WRONGTYPE")
		wantError(t, c.do("HLEN", "str"), "WRONGTYPE")
		wantError(t, c.do("HGETALL", "str"), "WRONGTYPE")
		wantError(t, c.do("GET", "all"), "WRONGTYPE")
		wantError(t, c.do("INCR", "all"), "WRONGTYPE")
		wantSimple(t, c.do("TYPE", "str"), "string")
	})

	t.Run("Should expire a hash and start a new one on the next HSET", func(t *testing.T) {
		c.do("HSET", "ttl", "a", "1", "b", "2")
		wantInt(t, c.do("EXPIRE", "ttl", "10"), 1)
		wantInt(t, c.do("TTL", "ttl"), 10)
		wantInt(t, c.do("HINCRBY", "ttl", "n", "1"), 1)
		wantInt(t, c.do("TTL", "ttl"), 10)
		clock.advance(11 * time.Second)
		wantInt(t, c.do("HLEN", "ttl"), 0)
		wantNull(t, c.do("HGET", "ttl", "a"))
		wantStrs(t, c.do("HGETALL", "ttl"))
		wantInt(t, c.do("HSET", "ttl", "fresh", "1"), 1)
		wantInt(t, c.do("TTL", "ttl"), -1)
		wantStrs(t, c.do("HGETALL", "ttl"), "fresh", "1")
	})

	t.Run("Should delete a hash with DEL and with SET", func(t *testing.T) {
		c.do("HSET", "g", "a", "1")
		wantInt(t, c.do("DEL", "g"), 1)
		wantInt(t, c.do("HLEN", "g"), 0)
		c.do("HSET", "g2", "a", "1")
		wantSimple(t, c.do("SET", "g2", "v", "XX"), "OK")
		wantBulk(t, c.do("GET", "g2"), "v")
	})

	t.Run("Should scan the fields of a hash with HSCAN", func(t *testing.T) {
		for i := range 45 {
			c.do("HSET", "big", fmt.Sprintf("f%03d", i), fmt.Sprint(i))
		}
		seen := map[string]string{}
		cursor := "0"
		for range 100 {
			v := c.do("HSCAN", "big", cursor, "COUNT", "8")
			next := string(v.Elems[0].Bytes)
			pairs := strs(t, v.Elems[1])
			if len(pairs)%2 != 0 {
				t.Fatalf("a page has %d elements", len(pairs))
			}
			for i := 0; i < len(pairs); i += 2 {
				seen[pairs[i]] = pairs[i+1]
			}
			if next == "0" {
				break
			}
			cursor = next
		}
		if len(seen) != 45 || seen["f044"] != "44" {
			t.Fatalf("HSCAN saw %d fields", len(seen))
		}
		v := c.do("HSCAN", "big", "0", "MATCH", "f00?", "COUNT", "100")
		if got := strs(t, v.Elems[1]); len(got) != 20 {
			t.Fatalf("MATCH gave %v", got)
		}
		wantError(t, c.do("HSCAN", "big", "abc"), "ERR invalid cursor")
		wantError(t, c.do("HSCAN", "big", "0", "COUNT", "0"), "ERR syntax")
		v = c.do("HSCAN", "missing", "0")
		if string(v.Elems[0].Bytes) != "0" || len(v.Elems[1].Elems) != 0 {
			t.Fatalf("HSCAN of a missing key gave %+v", v)
		}
	})

	t.Run("Should end an HSCAN when the hash was deleted and made again", func(t *testing.T) {
		for i := range 30 {
			c.do("HSET", "again", fmt.Sprintf("f%03d", i), "1")
		}
		v := c.do("HSCAN", "again", "0", "COUNT", "5")
		cursor := string(v.Elems[0].Bytes)
		if cursor == "0" {
			t.Fatal("the first page should not end the scan")
		}
		c.do("DEL", "again")
		c.do("HSET", "again", "other", "1")
		v = c.do("HSCAN", "again", cursor)
		if string(v.Elems[0].Bytes) != "0" || len(v.Elems[1].Elems) != 0 {
			t.Fatalf("got %+v", v)
		}
	})

	t.Run("Should count a hash as one key", func(t *testing.T) {
		d := dial(t, addr)
		keys := keyList(t, d.do("KEYS", "big"))
		if len(keys) != 1 {
			t.Fatalf("KEYS gave %v", keys)
		}
		page := scanAll(t, d, "MATCH", "big", "TYPE", "hash")
		if len(page) != 1 {
			t.Fatalf("SCAN gave %v", page)
		}
	})

	t.Run("Should refuse a field that is too large", func(t *testing.T) {
		_, addr := startServer(t, Options{MaxKey: 8})
		d := dial(t, addr)
		wantError(t, d.do("HSET", "h", "123456789", "v"), "ERR key is too large")
		wantInt(t, d.do("HSET", "h", "12345678", "v"), 1)
	})
}
