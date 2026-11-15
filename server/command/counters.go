// SPDX-License-Identifier: MIT

package command

import (
	"math"
	"strconv"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

var (
	replyOverflow  = resp.Error("ERR increment or decrement would overflow")
	replyValueBig  = resp.Error("ERR string exceeds maximum allowed size")
	maxStringBytes = resp.DefaultMaxBulk
)

// counterCommands returns INCR, DECR, INCRBY, DECRBY, APPEND, STRLEN, SETNX,
// GETSET and GETDEL.
func counterCommands() []*Spec {
	write := acl.CatWrite | acl.CatString | acl.CatFast
	return []*Spec{
		{Name: "incr", Arity: 2, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: incr},
		{Name: "decr", Arity: 2, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: decr},
		{Name: "incrby", Arity: 3, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: incrby},
		{Name: "decrby", Arity: 3, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: decrby},
		{Name: "append", Arity: 3, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: appendCmd},
		{Name: "strlen", Arity: 2, Categories: acl.CatRead | acl.CatString | acl.CatFast, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: strlen},
		{Name: "setnx", Arity: 3, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: setnx},
		{Name: "getset", Arity: 3, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: getset},
		{Name: "getdel", Arity: 2, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: getdel},
	}
}

// incr is INCR key.
func incr(env *Env, _ *Client, args [][]byte) resp.Value {
	return addInt(env, "incr", args[1], 1)
}

// decr is DECR key.
func decr(env *Env, _ *Client, args [][]byte) resp.Value {
	return addInt(env, "decr", args[1], -1)
}

// incrby is INCRBY key increment.
func incrby(env *Env, _ *Client, args [][]byte) resp.Value {
	delta, ok := parseInt(args[2])
	if !ok {
		return replyNotInt
	}
	return addInt(env, "incrby", args[1], delta)
}

// decrby is DECRBY key decrement.
func decrby(env *Env, _ *Client, args [][]byte) resp.Value {
	delta, ok := parseInt(args[2])
	if !ok {
		return replyNotInt
	}
	if delta == math.MinInt64 {
		return replyOverflow
	}
	return addInt(env, "decrby", args[1], -delta)
}

// addInt adds delta to the integer stored at key, as one atomic step. A missing
// key counts as zero.
func addInt(env *Env, name string, key []byte, delta int64) resp.Value {
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := env.Locks.Lock(key)
	defer env.Locks.Unlock(stripe)
	var cur, deadline int64
	old, deadline, err := env.load(key)
	switch {
	case err == nil:
		n, ok := parseInt(old)
		if !ok {
			return replyNotInt
		}
		cur = n
	case !isNotFound(err):
		return env.storageError(name, err)
	}
	if (delta > 0 && cur > math.MaxInt64-delta) || (delta < 0 && cur < math.MinInt64-delta) {
		return replyOverflow
	}
	cur += delta
	if err := env.save(key, strconv.AppendInt(nil, cur, 10), deadline); err != nil {
		return env.storageError(name, err)
	}
	return resp.Integer(cur)
}

// appendCmd is APPEND key value. It replies with the new length.
func appendCmd(env *Env, _ *Client, args [][]byte) resp.Value {
	key, suffix := args[1], args[2]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := env.Locks.Lock(key)
	defer env.Locks.Unlock(stripe)
	old, deadline, err := env.load(key)
	if err != nil && !isNotFound(err) {
		return env.storageError("append", err)
	}
	if len(old)+len(suffix) > maxStringBytes {
		return replyValueBig
	}
	joined := make([]byte, 0, len(old)+len(suffix))
	joined = append(append(joined, old...), suffix...)
	if err := env.save(key, joined, deadline); err != nil {
		return env.storageError("append", err)
	}
	return resp.Integer(int64(len(joined)))
}

// strlen is STRLEN key.
func strlen(env *Env, _ *Client, args [][]byte) resp.Value {
	if bad, ok := env.checkKey(args[1]); !ok {
		return bad
	}
	value, _, err := env.load(args[1])
	if isNotFound(err) {
		return resp.Integer(0)
	}
	if err != nil {
		return env.storageError("strlen", err)
	}
	return resp.Integer(int64(len(value)))
}

// setnx is SETNX key value. It replies 1 if the key was set and 0 if it exists.
func setnx(env *Env, _ *Client, args [][]byte) resp.Value {
	key := args[1]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := env.Locks.Lock(key)
	defer env.Locks.Unlock(stripe)
	if _, err := env.loadRecord(key); err == nil {
		return resp.Integer(0)
	} else if !isNotFound(err) {
		return env.storageError("setnx", err)
	}
	if err := env.save(key, args[2], 0); err != nil {
		return env.storageError("setnx", err)
	}
	return resp.Integer(1)
}

// getset is GETSET key value. It replies with the old value, or null.
func getset(env *Env, _ *Client, args [][]byte) resp.Value {
	key := args[1]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := env.Locks.Lock(key)
	defer env.Locks.Unlock(stripe)
	old, _, err := env.load(key)
	found := err == nil
	if err != nil && !isNotFound(err) {
		return env.storageError("getset", err)
	}
	if err := env.save(key, args[2], 0); err != nil {
		return env.storageError("getset", err)
	}
	return oldReply(true, found, old)
}

// getdel is GETDEL key. It replies with the old value, or null.
func getdel(env *Env, _ *Client, args [][]byte) resp.Value {
	key := args[1]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := env.Locks.Lock(key)
	defer env.Locks.Unlock(stripe)
	old, _, err := env.load(key)
	if isNotFound(err) {
		return resp.Null()
	}
	if err != nil {
		return env.storageError("getdel", err)
	}
	if err := env.Store.Delete(keyOf(key)); err != nil {
		return env.storageError("getdel", err)
	}
	return resp.Bulk(old)
}
