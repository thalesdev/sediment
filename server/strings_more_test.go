// SPDX-License-Identifier: MIT

package server

import (
	"fmt"
	"testing"
	"time"
)

func TestMoreStringCommands(t *testing.T) {
	clock := &fakeClock{}
	clock.ms.Store(1_700_000_000_000)
	_, addr := startServer(t, Options{Now: clock.now, SweepInterval: -1})
	c := dial(t, addr)

	t.Run("Should SETEX and PSETEX", func(t *testing.T) {
		wantSimple(t, c.do("SETEX", "k", "10", "v"), "OK")
		wantBulk(t, c.do("GET", "k"), "v")
		wantInt(t, c.do("TTL", "k"), 10)
		wantSimple(t, c.do("PSETEX", "p", "1500", "v"), "OK")
		wantInt(t, c.do("PTTL", "p"), 1500)
		wantError(t, c.do("SETEX", "k", "0", "v"), "ERR invalid expire time in 'setex' command")
		wantError(t, c.do("PSETEX", "k", "-5", "v"), "ERR invalid expire time in 'psetex' command")
		wantError(t, c.do("SETEX", "k", "x", "v"), "ERR value is not an integer")
		clock.advance(11 * time.Second)
		wantNull(t, c.do("GET", "k"))
	})

	t.Run("Should MSETNX all or nothing", func(t *testing.T) {
		wantInt(t, c.do("MSETNX", "m1", "a", "m2", "b"), 1)
		wantBulk(t, c.do("GET", "m2"), "b")
		wantInt(t, c.do("MSETNX", "m3", "c", "m2", "x"), 0)
		wantNull(t, c.do("GET", "m3"))
		wantBulk(t, c.do("GET", "m2"), "b")
		c.do("HSET", "mh", "f", "1")
		wantInt(t, c.do("MSETNX", "mh", "x", "m9", "y"), 0)
		wantNull(t, c.do("GET", "m9"))
		wantError(t, c.do("MSETNX", "a", "1", "b"), "ERR wrong number")
	})

	t.Run("Should GETEX with each option", func(t *testing.T) {
		c.do("SET", "g", "v")
		wantBulk(t, c.do("GETEX", "g"), "v")
		wantInt(t, c.do("TTL", "g"), -1)
		wantBulk(t, c.do("GETEX", "g", "EX", "10"), "v")
		wantInt(t, c.do("TTL", "g"), 10)
		wantBulk(t, c.do("GETEX", "g", "PX", "2500"), "v")
		wantInt(t, c.do("PTTL", "g"), 2500)
		wantBulk(t, c.do("GETEX", "g", "PERSIST"), "v")
		wantInt(t, c.do("TTL", "g"), -1)
		now := clock.ms.Load()
		wantBulk(t, c.do("GETEX", "g", "PXAT", itoa(now+4000)), "v")
		wantInt(t, c.do("PTTL", "g"), 4000)
		wantBulk(t, c.do("GETEX", "g", "EXAT", itoa(now/1000-100)), "v")
		wantNull(t, c.do("GET", "g"))
		wantNull(t, c.do("GETEX", "missing", "EX", "10"))
		wantError(t, c.do("GETEX", "g", "EX"), "ERR syntax")
		wantError(t, c.do("GETEX", "g", "BAD", "1"), "ERR syntax")
		wantError(t, c.do("GETEX", "g", "EX", "0"), "ERR invalid expire time in 'getex' command")
		wantError(t, c.do("GETEX", "g", "PERSIST", "1"), "ERR syntax")
	})

	t.Run("Should GETRANGE like Redis", func(t *testing.T) {
		c.do("SET", "r", "This is a string")
		for _, tc := range []struct{ start, end, want string }{
			{"0", "3", "This"}, {"-3", "-1", "ing"}, {"0", "-1", "This is a string"}, {"10", "100", "string"},
			{"5", "3", ""}, {"-1", "-3", ""}, {"-100", "2", "Thi"}, {"100", "200", ""},
		} {
			wantBulk(t, c.do("GETRANGE", "r", tc.start, tc.end), tc.want)
		}
		wantBulk(t, c.do("GETRANGE", "missing", "0", "5"), "")
		wantError(t, c.do("GETRANGE", "r", "a", "1"), "ERR value is not an integer")
	})

	t.Run("Should SETRANGE and pad with zero bytes", func(t *testing.T) {
		c.do("SET", "sr", "Hello World")
		wantInt(t, c.do("SETRANGE", "sr", "6", "Redis"), 11)
		wantBulk(t, c.do("GET", "sr"), "Hello Redis")
		wantInt(t, c.do("SETRANGE", "pad", "3", "ab"), 5)
		wantBulk(t, c.do("GET", "pad"), "\x00\x00\x00ab")
		wantInt(t, c.do("SETRANGE", "sr", "20", ""), 11)
		wantInt(t, c.do("SETRANGE", "nokey", "5", ""), 0)
		wantNull(t, c.do("GET", "nokey"))
		wantError(t, c.do("SETRANGE", "sr", "-1", "x"), "ERR offset is out of range")
		wantError(t, c.do("SETRANGE", "sr", "99999999999", "x"), "ERR string exceeds maximum allowed size")
		c.do("SETEX", "srt", "10", "abc")
		wantInt(t, c.do("SETRANGE", "srt", "1", "X"), 3)
		wantInt(t, c.do("TTL", "srt"), 10)
	})

	t.Run("Should INCRBYFLOAT", func(t *testing.T) {
		wantBulk(t, c.do("INCRBYFLOAT", "f", "10.5"), "10.5")
		wantBulk(t, c.do("INCRBYFLOAT", "f", "-0.25"), "10.25")
		c.do("SET", "f2", "3.0e3")
		wantBulk(t, c.do("INCRBYFLOAT", "f2", "1"), "3001")
		c.do("SET", "fs", "abc")
		wantError(t, c.do("INCRBYFLOAT", "fs", "1"), "ERR value is not a valid float")
		wantError(t, c.do("INCRBYFLOAT", "f", "x"), "ERR value is not a valid float")
		wantError(t, c.do("INCRBYFLOAT", "f", "nan"), "ERR value is not a valid float")
		c.do("SET", "fbig", "1.7976931348623157e308")
		wantError(t, c.do("INCRBYFLOAT", "fbig", "1.7976931348623157e308"), "ERR increment would produce NaN or Infinity")
		c.do("SETEX", "ft", "10", "1")
		wantBulk(t, c.do("INCRBYFLOAT", "ft", "1"), "2")
		wantInt(t, c.do("TTL", "ft"), 10)
	})

	t.Run("Should refuse a string command on a collection", func(t *testing.T) {
		c.do("HSET", "wt", "f", "1")
		for _, args := range [][]string{{"GETRANGE", "wt", "0", "1"}, {"SETRANGE", "wt", "0", "x"}, {"INCRBYFLOAT", "wt", "1"}, {"GETEX", "wt", "EX", "5"}} {
			wantError(t, c.do(args...), "WRONGTYPE")
		}
	})

	t.Run("Should UNLINK like DEL", func(t *testing.T) {
		c.do("SET", "u1", "1")
		c.do("HSET", "u2", "f", "1")
		wantInt(t, c.do("UNLINK", "u1", "u2", "nope"), 2)
		wantInt(t, c.do("EXISTS", "u1", "u2"), 0)
	})

	t.Run("Should RENAME a string and keep its time", func(t *testing.T) {
		c.do("SETEX", "old", "30", "v")
		wantSimple(t, c.do("RENAME", "old", "new"), "OK")
		wantNull(t, c.do("GET", "old"))
		wantBulk(t, c.do("GET", "new"), "v")
		wantInt(t, c.do("TTL", "new"), 30)
		c.do("SET", "target", "x")
		wantSimple(t, c.do("RENAME", "new", "target"), "OK")
		wantBulk(t, c.do("GET", "target"), "v")
		wantError(t, c.do("RENAME", "missing", "x"), "ERR no such key")
		wantSimple(t, c.do("RENAME", "target", "target"), "OK")
		wantBulk(t, c.do("GET", "target"), "v")
	})

	t.Run("Should RENAMENX only to a key that does not exist", func(t *testing.T) {
		c.do("SET", "nx1", "a")
		c.do("SET", "nx2", "b")
		wantInt(t, c.do("RENAMENX", "nx1", "nx2"), 0)
		wantBulk(t, c.do("GET", "nx1"), "a")
		wantInt(t, c.do("RENAMENX", "nx1", "nx3"), 1)
		wantBulk(t, c.do("GET", "nx3"), "a")
		wantNull(t, c.do("GET", "nx1"))
		wantError(t, c.do("RENAMENX", "missing", "x"), "ERR no such key")
		wantInt(t, c.do("RENAMENX", "nx3", "nx3"), 0)
	})

	t.Run("Should RENAME a hash in the same slot without copying and in another slot with a copy", func(t *testing.T) {
		for _, pair := range [][2]string{{"{t}src", "{t}dst"}, {"hash-src", "hash-dst"}} {
			src, dst := pair[0], pair[1]
			for i := range 40 {
				c.do("HSET", src, fmt.Sprintf("f%02d", i), fmt.Sprint(i))
			}
			c.do("EXPIRE", src, "100")
			c.do("SET", dst, "old")
			wantSimple(t, c.do("RENAME", src, dst), "OK")
			wantSimple(t, c.do("TYPE", src), "none")
			wantSimple(t, c.do("TYPE", dst), "hash")
			wantInt(t, c.do("HLEN", dst), 40)
			wantBulk(t, c.do("HGET", dst, "f39"), "39")
			wantInt(t, c.do("TTL", dst), 100)
			c.do("HSET", dst, "extra", "1")
			wantInt(t, c.do("HLEN", dst), 41)
			wantInt(t, c.do("HLEN", src), 0)
		}
	})

	t.Run("Should RENAME over a collection and remove its elements", func(t *testing.T) {
		c.do("SADD", "{x}a", "1", "2")
		c.do("SADD", "{x}b", "3", "4", "5")
		wantSimple(t, c.do("RENAME", "{x}a", "{x}b"), "OK")
		wantStrs(t, c.do("SMEMBERS", "{x}b"), "1", "2")
		wantInt(t, c.do("SCARD", "{x}b"), 2)
	})
}
