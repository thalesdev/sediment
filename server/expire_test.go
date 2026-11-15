// SPDX-License-Identifier: MIT

package server

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct{ ms atomic.Int64 }

func (f *fakeClock) now() time.Time { return time.UnixMilli(f.ms.Load()) }

func (f *fakeClock) advance(d time.Duration) { f.ms.Add(d.Milliseconds()) }

func TestExpiry(t *testing.T) {
	clock := &fakeClock{}
	clock.ms.Store(1_700_000_000_000)
	_, addr := startServer(t, Options{Now: clock.now})
	c := dial(t, addr)

	t.Run("Should expire a key set with EX", func(t *testing.T) {
		wantSimple(t, c.do("SET", "k", "v", "EX", "10"), "OK")
		wantBulk(t, c.do("GET", "k"), "v")
		wantInt(t, c.do("TTL", "k"), 10)
		wantInt(t, c.do("PTTL", "k"), 10000)
		clock.advance(9 * time.Second)
		wantBulk(t, c.do("GET", "k"), "v")
		clock.advance(time.Second)
		wantNull(t, c.do("GET", "k"))
		wantInt(t, c.do("EXISTS", "k"), 0)
		wantInt(t, c.do("TTL", "k"), -2)
	})

	t.Run("Should honor PX EXAT and PXAT", func(t *testing.T) {
		c.do("SET", "px", "v", "PX", "1500")
		wantInt(t, c.do("PTTL", "px"), 1500)
		now := clock.ms.Load()
		c.do("SET", "exat", "v", "EXAT", itoa(now/1000+5))
		wantInt(t, c.do("TTL", "exat"), 5)
		c.do("SET", "pxat", "v", "PXAT", itoa(now+2500))
		wantInt(t, c.do("PTTL", "pxat"), 2500)
		wantInt(t, c.do("EXPIRETIME", "exat"), now/1000+5)
		wantInt(t, c.do("PEXPIRETIME", "pxat"), now+2500)
	})

	t.Run("Should refuse bad SET expiry options", func(t *testing.T) {
		wantError(t, c.do("SET", "k", "v", "EX", "0"), "ERR invalid expire time")
		wantError(t, c.do("SET", "k", "v", "EX", "-1"), "ERR invalid expire time")
		wantError(t, c.do("SET", "k", "v", "EX", "9223372036854775807"), "ERR invalid expire time")
		wantError(t, c.do("SET", "k", "v", "EX", "x"), "ERR value is not an integer")
		wantError(t, c.do("SET", "k", "v", "EX"), "ERR syntax")
		wantError(t, c.do("SET", "k", "v", "EX", "1", "PX", "1"), "ERR syntax")
		wantError(t, c.do("SET", "k", "v", "EX", "1", "KEEPTTL"), "ERR syntax")
	})

	t.Run("Should clear the TTL on a plain SET and on MSET", func(t *testing.T) {
		c.do("SET", "p", "v", "EX", "10")
		c.do("SET", "p", "w")
		wantInt(t, c.do("TTL", "p"), -1)
		c.do("SET", "q", "v", "EX", "10")
		c.do("MSET", "q", "w")
		wantInt(t, c.do("TTL", "q"), -1)
	})

	t.Run("Should keep the TTL with KEEPTTL", func(t *testing.T) {
		c.do("SET", "kt", "v", "EX", "10")
		clock.advance(4 * time.Second)
		wantSimple(t, c.do("SET", "kt", "w", "KEEPTTL"), "OK")
		wantBulk(t, c.do("GET", "kt"), "w")
		wantInt(t, c.do("TTL", "kt"), 6)
	})

	t.Run("Should EXPIRE, PEXPIRE and PERSIST", func(t *testing.T) {
		wantInt(t, c.do("EXPIRE", "none", "10"), 0)
		c.do("SET", "e", "v")
		wantInt(t, c.do("TTL", "e"), -1)
		wantInt(t, c.do("EXPIRE", "e", "10"), 1)
		wantInt(t, c.do("TTL", "e"), 10)
		wantInt(t, c.do("PEXPIRE", "e", "500"), 1)
		wantInt(t, c.do("PTTL", "e"), 500)
		wantInt(t, c.do("PERSIST", "e"), 1)
		wantInt(t, c.do("PERSIST", "e"), 0)
		wantInt(t, c.do("TTL", "e"), -1)
		wantBulk(t, c.do("GET", "e"), "v")
	})

	t.Run("Should delete the key on a time in the past", func(t *testing.T) {
		c.do("SET", "gone", "v")
		wantInt(t, c.do("EXPIRE", "gone", "-1"), 1)
		wantNull(t, c.do("GET", "gone"))
		c.do("SET", "gone2", "v")
		wantInt(t, c.do("EXPIREAT", "gone2", "1"), 1)
		wantNull(t, c.do("GET", "gone2"))
	})

	t.Run("Should honor NX XX GT and LT of EXPIRE", func(t *testing.T) {
		c.do("SET", "f", "v")
		wantInt(t, c.do("EXPIRE", "f", "10", "XX"), 0)
		wantInt(t, c.do("EXPIRE", "f", "10", "GT"), 0)
		wantInt(t, c.do("EXPIRE", "f", "10", "NX"), 1)
		wantInt(t, c.do("EXPIRE", "f", "20", "NX"), 0)
		wantInt(t, c.do("EXPIRE", "f", "5", "GT"), 0)
		wantInt(t, c.do("EXPIRE", "f", "20", "GT"), 1)
		wantInt(t, c.do("EXPIRE", "f", "30", "LT"), 0)
		wantInt(t, c.do("EXPIRE", "f", "15", "LT"), 1)
		wantInt(t, c.do("TTL", "f"), 15)
		wantError(t, c.do("EXPIRE", "f", "1", "NX", "XX"), "ERR NX and XX")
		wantError(t, c.do("EXPIRE", "f", "1", "GT", "LT"), "ERR GT and LT")
		wantError(t, c.do("EXPIRE", "f", "x"), "ERR value is not an integer")
	})

	t.Run("Should keep the TTL on INCR and APPEND and drop it on GETSET", func(t *testing.T) {
		c.do("SET", "n", "1", "EX", "10")
		wantInt(t, c.do("INCR", "n"), 2)
		wantInt(t, c.do("TTL", "n"), 10)
		wantInt(t, c.do("APPEND", "n", "0"), 2)
		wantInt(t, c.do("TTL", "n"), 10)
		wantBulk(t, c.do("GETSET", "n", "x"), "20")
		wantInt(t, c.do("TTL", "n"), -1)
	})

	t.Run("Should treat an expired key as missing for writes", func(t *testing.T) {
		c.do("SET", "x", "5", "EX", "1")
		clock.advance(2 * time.Second)
		wantInt(t, c.do("INCR", "x"), 1)
		wantInt(t, c.do("TTL", "x"), -1)
		c.do("SET", "y", "v", "EX", "1")
		clock.advance(2 * time.Second)
		wantInt(t, c.do("SETNX", "y", "new"), 1)
		wantNull(t, c.do("SET", "z", "v", "XX"))
		wantInt(t, c.do("DEL", "y"), 1)
	})

	t.Run("Should write plain pipelined SET commands without a TTL", func(t *testing.T) {
		c.do("SET", "pl", "v", "EX", "10")
		c.send("SET", "pl", "a")
		c.send("SET", "pl2", "b")
		c.read()
		c.read()
		wantInt(t, c.do("TTL", "pl"), -1)
		wantBulk(t, c.do("GET", "pl2"), "b")
	})
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
