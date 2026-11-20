// SPDX-License-Identifier: MIT

package server

import (
	"testing"
)

func TestDigestAndConditions(t *testing.T) {
	t.Run("Should return the XXH3 digest of a string as 16 hex digits, null for a missing key", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantNull(t, c.do("DIGEST", "nope"))
		wantSimple(t, c.do("SET", "foo", "v8lf0c11xh8ymlqztfd3eeq16kfn4sspw7fqmnuuq3k3t75em5wdizgcdw7uc26nnf961u2jkfzkjytls2kwlj7626sd"), "OK")
		wantBulk(t, c.do("DIGEST", "foo"), "00006c38adf31777")
		wantInt(t, c.do("HSET", "h", "a", "1"), 1)
		wantError(t, c.do("DIGEST", "h"), "WRONGTYPE")
	})
	t.Run("Should set with IFEQ, IFNE, IFDEQ and IFDNE, with and without GET", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "k", "hello"), "OK")
		wantSimple(t, c.do("SET", "k", "world", "IFEQ", "hello"), "OK")
		wantNull(t, c.do("SET", "k", "x", "IFEQ", "hello"))
		wantBulk(t, c.do("GET", "k"), "world")
		wantNull(t, c.do("SET", "k", "x", "IFNE", "world"))
		wantSimple(t, c.do("SET", "k", "y", "IFNE", "other"), "OK")
		wantSimple(t, c.do("SET", "new", "v", "IFNE", "x"), "OK")
		wantNull(t, c.do("SET", "missing", "v", "IFEQ", "x"))
		wantInt(t, c.do("EXISTS", "missing"), 0)
		d := text(c.do("DIGEST", "k"))
		wantSimple(t, c.do("SET", "k", "z", "IFDEQ", d), "OK")
		wantNull(t, c.do("SET", "k", "q", "IFDEQ", d))
		wantSimple(t, c.do("SET", "k", "w", "IFDNE", d), "OK")
		wantBulk(t, c.do("SET", "k", "a", "IFEQ", "w", "GET"), "w")
		wantBulk(t, c.do("SET", "k", "b", "IFEQ", "nope", "GET"), "a")
		wantBulk(t, c.do("GET", "k"), "a")
		wantSimple(t, c.do("SET", "k", "c", "IFEQ", "a", "EX", "100"), "OK")
		if v := c.do("TTL", "k"); v.Int < 90 {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("Should refuse a condition with NX, XX or another condition, and a digest that is not 16 hex digits", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "k", "hello"), "OK")
		wantError(t, c.do("SET", "k", "x", "NX", "IFEQ", "hello"), "ERR syntax error")
		wantError(t, c.do("SET", "k", "x", "IFEQ", "hello", "XX"), "ERR syntax error")
		wantError(t, c.do("SET", "k", "x", "IFEQ", "hello", "IFNE", "y"), "ERR syntax error")
		wantError(t, c.do("SET", "k", "x", "IFEQ"), "ERR syntax error")
		wantError(t, c.do("SET", "k", "x", "IFDEQ", "abc"), "ERR The digest must be exactly 16")
		wantError(t, c.do("DELEX", "k", "IFDNE", ""), "ERR The digest must be exactly 16")
		wantBulk(t, c.do("GET", "k"), "hello")
		wantInt(t, c.do("SADD", "s", "m"), 1)
		wantError(t, c.do("SET", "s", "v", "IFEQ", "m"), "WRONGTYPE")
	})
	t.Run("Should accept an upper case digest", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "k", "hello"), "OK")
		d := text(c.do("DIGEST", "k"))
		wantSimple(t, c.do("SET", "k", "x", "IFDEQ", upper(d)), "OK")
	})
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'f' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func TestDelex(t *testing.T) {
	t.Run("Should delete a key that passes the condition and answer 1 or 0", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "k", "hello"), "OK")
		wantInt(t, c.do("DELEX", "k", "IFEQ", "world"), 0)
		wantInt(t, c.do("DELEX", "k", "IFNE", "hello"), 0)
		wantInt(t, c.do("DELEX", "k", "IFNE", "world"), 1)
		wantInt(t, c.do("DELEX", "k", "IFEQ", "x"), 0)
		wantSimple(t, c.do("SET", "k", "hello"), "OK")
		d := text(c.do("DIGEST", "k"))
		wantInt(t, c.do("DELEX", "k", "IFDNE", d), 0)
		wantInt(t, c.do("DELEX", "k", "IFDEQ", d), 1)
		wantInt(t, c.do("EXISTS", "k"), 0)
		wantInt(t, c.do("DELEX", "nope", "IFDEQ", "1234567890"), 0)
	})
	t.Run("Should be DEL with no condition, and refuse a bad condition or a wrong count", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("HSET", "h", "a", "1"), 1)
		wantInt(t, c.do("DELEX", "h"), 1)
		wantInt(t, c.do("DBSIZE"), 0)
		wantSimple(t, c.do("SET", "k", "v"), "OK")
		wantError(t, c.do("DELEX", "k", "INVALID", "v"), "ERR Invalid condition")
		wantError(t, c.do("DELEX", "k", "IFEQ"), "ERR wrong number of arguments")
		wantError(t, c.do("DELEX", "k", "IFEQ", "v", "extra"), "ERR wrong number of arguments")
		wantInt(t, c.do("HSET", "h", "a", "1"), 1)
		wantError(t, c.do("DELEX", "h", "IFEQ", "1"), "ERR")
	})
}

func TestMsetex(t *testing.T) {
	t.Run("Should set many keys with one expiry, in any order of the options", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("MSETEX", "2", "a", "1", "b", "2", "EX", "100"), 1)
		wantBulk(t, c.do("GET", "b"), "2")
		if v := c.do("TTL", "a"); v.Int < 90 {
			t.Fatalf("got %+v", v)
		}
		wantInt(t, c.do("MSETEX", "1", "c", "3", "NX", "PX", "50000"), 1)
		wantInt(t, c.do("MSETEX", "1", "c", "4", "px", "50000", "nx"), 0)
		wantBulk(t, c.do("GET", "c"), "3")
	})
	t.Run("Should apply NX and XX to all the keys, and keep the TTL with KEEPTTL", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "x", "old"), "OK")
		wantInt(t, c.do("MSETEX", "2", "x", "1", "y", "2", "NX"), 0)
		wantNull(t, c.do("GET", "y"))
		wantInt(t, c.do("MSETEX", "2", "x", "1", "y", "2", "XX"), 0)
		wantInt(t, c.do("MSETEX", "1", "x", "new", "XX"), 1)
		wantSimple(t, c.do("SETEX", "t", "100", "old"), "OK")
		wantInt(t, c.do("MSETEX", "1", "t", "new", "KEEPTTL"), 1)
		if v := c.do("TTL", "t"); v.Int < 90 {
			t.Fatalf("got %+v", v)
		}
	})
	t.Run("Should refuse a bad count, a missing pair and options that exclude each other", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantError(t, c.do("MSETEX"), "ERR wrong number of arguments")
		wantError(t, c.do("MSETEX", "key1", "v", "EX", "10"), "ERR invalid numkeys value")
		wantError(t, c.do("MSETEX", "2147483648", "k", "v", "EX", "10"), "ERR invalid numkeys value")
		wantError(t, c.do("MSETEX", "2147483647", "k", "v", "EX", "10"), "ERR wrong number of key-value pairs")
		wantError(t, c.do("MSETEX", "2", "k", "v", "j"), "ERR wrong number of key-value pairs")
		wantError(t, c.do("MSETEX", "1", "k", "v", "BAD"), "ERR syntax error")
		wantError(t, c.do("MSETEX", "1", "k", "v", "NX", "XX"), "ERR syntax error")
		wantError(t, c.do("MSETEX", "1", "k", "v", "EX", "10", "PX", "5000"), "ERR syntax error")
		wantError(t, c.do("MSETEX", "1", "k", "v", "KEEPTTL", "EX", "10"), "ERR syntax error")
		wantInt(t, c.do("DBSIZE"), 0)
	})
}

func TestBits(t *testing.T) {
	t.Run("Should set and get bits, most significant bit first, and answer the old bit", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantInt(t, c.do("SETBIT", "k", "1", "1"), 0)
		wantBulk(t, c.do("GET", "k"), "\x40")
		wantInt(t, c.do("SETBIT", "k", "2", "1"), 0)
		wantInt(t, c.do("SETBIT", "k", "1", "0"), 1)
		wantBulk(t, c.do("GET", "k"), "\x20")
		wantInt(t, c.do("GETBIT", "k", "2"), 1)
		wantInt(t, c.do("GETBIT", "k", "1"), 0)
		wantInt(t, c.do("GETBIT", "k", "100"), 0)
		wantInt(t, c.do("GETBIT", "nope", "0"), 0)
		wantInt(t, c.do("SETBIT", "k", "15", "1"), 0)
		wantBulk(t, c.do("GET", "k"), "\x20\x01")
	})
	t.Run("Should keep the TTL and refuse a bad offset, a bad bit and a wrong type", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SETEX", "k", "100", "a"), "OK")
		wantInt(t, c.do("SETBIT", "k", "0", "1"), 0)
		if v := c.do("TTL", "k"); v.Int < 90 {
			t.Fatalf("got %+v", v)
		}
		wantError(t, c.do("SETBIT", "k", "-1", "1"), "ERR bit offset is not an integer or out of range")
		wantError(t, c.do("SETBIT", "k", "4294967296", "1"), "ERR bit offset is not an integer or out of range")
		wantError(t, c.do("SETBIT", "k", "0", "2"), "ERR bit is not an integer or out of range")
		wantError(t, c.do("SETBIT", "k", "0", "-1"), "ERR bit is not an integer or out of range")
		wantError(t, c.do("GETBIT", "k", "x"), "ERR bit offset is not an integer or out of range")
		wantInt(t, c.do("SADD", "s", "m"), 1)
		wantError(t, c.do("SETBIT", "s", "0", "1"), "WRONGTYPE")
		wantError(t, c.do("GETBIT", "s", "0"), "WRONGTYPE")
		wantError(t, c.do("SETBIT", "k", "4294967295", "1"), "ERR")
	})
}

func TestLCS(t *testing.T) {
	setup := func(t *testing.T) *client {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("MSET", "k1", "ohmytext", "k2", "mynewtext"), "OK")
		return c
	}
	t.Run("Should reply with the subsequence, its length, or the ranges", func(t *testing.T) {
		c := setup(t)
		wantBulk(t, c.do("LCS", "k1", "k2"), "mytext")
		wantInt(t, c.do("LCS", "k1", "k2", "LEN"), 6)
		v := c.do("LCS", "k1", "k2", "IDX")
		if len(v.Elems) != 4 || text(v.Elems[0]) != "matches" || v.Elems[3].Int != 6 {
			t.Fatalf("got %+v", v)
		}
		matches := v.Elems[1].Elems
		if len(matches) != 2 || matches[0].Elems[0].Elems[0].Int != 4 || matches[0].Elems[1].Elems[0].Int != 5 || matches[1].Elems[0].Elems[0].Int != 2 {
			t.Fatalf("got %+v", matches)
		}
		with := c.do("LCS", "k1", "k2", "IDX", "WITHMATCHLEN", "MINMATCHLEN", "4").Elems[1].Elems
		if len(with) != 1 || len(with[0].Elems) != 3 || with[0].Elems[2].Int != 4 {
			t.Fatalf("got %+v", with)
		}
	})
	t.Run("Should treat a missing key as an empty string and refuse LEN with IDX and a wrong type", func(t *testing.T) {
		c := setup(t)
		wantBulk(t, c.do("LCS", "k1", "nope"), "")
		wantInt(t, c.do("LCS", "nope", "nope2", "LEN"), 0)
		wantError(t, c.do("LCS", "k1", "k2", "LEN", "IDX"), "ERR If you want both the length and indexes")
		wantError(t, c.do("LCS", "k1", "k2", "WAT"), "ERR syntax error")
		wantInt(t, c.do("SADD", "s", "m"), 1)
		wantError(t, c.do("LCS", "k1", "s"), "ERR The specified keys must contain string values")
	})
}

func TestSubstr(t *testing.T) {
	t.Run("Should be GETRANGE", func(t *testing.T) {
		_, addr := startServer(t, Options{})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "key", "abcde"), "OK")
		wantBulk(t, c.do("SUBSTR", "key", "0", "3"), "abcd")
		wantBulk(t, c.do("SUBSTR", "key", "-4", "-1"), "bcde")
		wantBulk(t, c.do("SUBSTR", "key", "-1", "-3"), "")
	})
}
