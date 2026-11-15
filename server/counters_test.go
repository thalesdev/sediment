// SPDX-License-Identifier: MIT

package server

import "testing"

func TestCounterCommands(t *testing.T) {
	_, addr := startServer(t, Options{})
	c := dial(t, addr)

	t.Run("Should INCR and DECR from a missing key", func(t *testing.T) {
		wantInt(t, c.do("INCR", "c1"), 1)
		wantInt(t, c.do("INCR", "c1"), 2)
		wantInt(t, c.do("DECR", "c1"), 1)
		wantInt(t, c.do("DECR", "c2"), -1)
		wantBulk(t, c.do("GET", "c1"), "1")
	})

	t.Run("Should INCRBY and DECRBY", func(t *testing.T) {
		wantInt(t, c.do("INCRBY", "c3", "10"), 10)
		wantInt(t, c.do("INCRBY", "c3", "-4"), 6)
		wantInt(t, c.do("DECRBY", "c3", "10"), -4)
		wantError(t, c.do("INCRBY", "c3", "x"), "ERR value is not an integer")
		wantError(t, c.do("DECRBY", "c3", "-9223372036854775808"), "ERR increment or decrement would overflow")
	})

	t.Run("Should refuse a value that is not an integer", func(t *testing.T) {
		c.do("SET", "s", "abc")
		wantError(t, c.do("INCR", "s"), "ERR value is not an integer")
		wantBulk(t, c.do("GET", "s"), "abc")
	})

	t.Run("Should refuse an overflow and keep the value", func(t *testing.T) {
		c.do("SET", "max", "9223372036854775807")
		wantError(t, c.do("INCR", "max"), "ERR increment or decrement would overflow")
		wantBulk(t, c.do("GET", "max"), "9223372036854775807")
		c.do("SET", "min", "-9223372036854775808")
		wantError(t, c.do("DECR", "min"), "ERR increment or decrement would overflow")
	})

	t.Run("Should APPEND and STRLEN", func(t *testing.T) {
		wantInt(t, c.do("STRLEN", "a"), 0)
		wantInt(t, c.do("APPEND", "a", "hello"), 5)
		wantInt(t, c.do("APPEND", "a", " world"), 11)
		wantBulk(t, c.do("GET", "a"), "hello world")
		wantInt(t, c.do("STRLEN", "a"), 11)
	})

	t.Run("Should SETNX only when the key is missing", func(t *testing.T) {
		wantInt(t, c.do("SETNX", "nx", "1"), 1)
		wantInt(t, c.do("SETNX", "nx", "2"), 0)
		wantBulk(t, c.do("GET", "nx"), "1")
	})

	t.Run("Should GETSET and GETDEL", func(t *testing.T) {
		wantNull(t, c.do("GETSET", "g", "1"))
		wantBulk(t, c.do("GETSET", "g", "2"), "1")
		wantBulk(t, c.do("GETDEL", "g"), "2")
		wantNull(t, c.do("GETDEL", "g"))
		wantNull(t, c.do("GET", "g"))
	})

	t.Run("Should not lose increments made at the same time", func(t *testing.T) {
		const workers, each = 8, 50
		done := make(chan struct{}, workers)
		for range workers {
			go func() {
				w := dial(t, addr)
				for range each {
					w.do("INCR", "race")
				}
				done <- struct{}{}
			}()
		}
		for range workers {
			<-done
		}
		wantBulk(t, c.do("GET", "race"), "400")
	})

	t.Run("Should check the number of arguments", func(t *testing.T) {
		wantError(t, c.do("INCR"), "ERR wrong number")
		wantError(t, c.do("APPEND", "a"), "ERR wrong number")
		wantError(t, c.do("GETDEL", "a", "b"), "ERR wrong number")
	})
}
