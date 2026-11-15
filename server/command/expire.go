// SPDX-License-Identifier: MIT

package command

import (
	"strings"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

var (
	replyFlagsNXOther = resp.Error("ERR NX and XX, GT or LT options at the same time are not compatible")
	replyFlagsGTLT    = resp.Error("ERR GT and LT options at the same time are not compatible")
)

// expireCommands returns EXPIRE, PEXPIRE, EXPIREAT, PEXPIREAT, TTL, PTTL,
// EXPIRETIME, PEXPIRETIME and PERSIST.
func expireCommands() []*Spec {
	write := acl.CatWrite | acl.CatKeyspace | acl.CatFast
	read := acl.CatRead | acl.CatKeyspace | acl.CatFast
	return []*Spec{
		{Name: "expire", Arity: -3, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: expireHandler("expire", 1000, false)},
		{Name: "pexpire", Arity: -3, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: expireHandler("pexpire", 1, false)},
		{Name: "expireat", Arity: -3, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: expireHandler("expireat", 1000, true)},
		{Name: "pexpireat", Arity: -3, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: expireHandler("pexpireat", 1, true)},
		{Name: "ttl", Arity: 2, Categories: read, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: ttlHandler("ttl", 1000, false)},
		{Name: "pttl", Arity: 2, Categories: read, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: ttlHandler("pttl", 1, false)},
		{Name: "expiretime", Arity: 2, Categories: read, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: ttlHandler("expiretime", 1000, true)},
		{Name: "pexpiretime", Arity: 2, Categories: read, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: ttlHandler("pexpiretime", 1, true)},
		{Name: "persist", Arity: 2, Categories: write, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: persist},
	}
}

// expireHandler returns the handler of EXPIRE, PEXPIRE, EXPIREAT or PEXPIREAT.
// The unit is the number of milliseconds in one unit of the argument, and
// absolute tells that the argument is a Unix time and not a time to wait.
func expireHandler(name string, unit int64, absolute bool) Handler {
	return func(env *Env, _ *Client, args [][]byte) resp.Value {
		key := args[1]
		n, ok := parseInt(args[2])
		if !ok {
			return replyNotInt
		}
		var nx, xx, gt, lt bool
		for _, opt := range args[3:] {
			switch strings.ToUpper(string(opt)) {
			case "NX":
				nx = true
			case "XX":
				xx = true
			case "GT":
				gt = true
			case "LT":
				lt = true
			default:
				return resp.Error("ERR Unsupported option " + printable(opt, 32))
			}
		}
		if nx && (xx || gt || lt) {
			return replyFlagsNXOther
		}
		if gt && lt {
			return replyFlagsGTLT
		}
		if bad, ok := env.checkKey(key); !ok {
			return bad
		}
		now := env.nowMillis()
		var deadline int64
		var past bool
		switch {
		case n <= 0:
			past = true
		case absolute:
			deadline, ok = deadlineAt(0, n, unit)
		default:
			deadline, ok = deadlineAt(now, n, unit)
		}
		if !ok {
			return resp.Error("ERR invalid expire time in '" + name + "' command")
		}
		if deadline <= now {
			past = true
		}

		stripe := env.Locks.Lock(key)
		defer env.Locks.Unlock(stripe)
		value, current, err := env.load(key)
		if isNotFound(err) {
			return resp.Integer(0)
		}
		if err != nil {
			return env.storageError(name, err)
		}
		hasTTL := current != 0
		if (nx && hasTTL) || (xx && !hasTTL) {
			return resp.Integer(0)
		}
		target := deadline
		if past {
			target = 0
		}
		if gt && (!hasTTL || (!past && deadline <= current)) {
			return resp.Integer(0)
		}
		if gt && past {
			return resp.Integer(0)
		}
		if lt && hasTTL && !past && deadline >= current {
			return resp.Integer(0)
		}
		if past {
			if err := env.Store.Delete(key); err != nil {
				return env.storageError(name, err)
			}
			return resp.Integer(1)
		}
		if err := env.save(key, value, target); err != nil {
			return env.storageError(name, err)
		}
		return resp.Integer(1)
	}
}

// ttlHandler returns the handler of TTL, PTTL, EXPIRETIME or PEXPIRETIME. The
// reply is -2 for a missing key and -1 for a key that does not expire.
func ttlHandler(name string, unit int64, absolute bool) Handler {
	return func(env *Env, _ *Client, args [][]byte) resp.Value {
		if bad, ok := env.checkKey(args[1]); !ok {
			return bad
		}
		_, deadline, err := env.load(args[1])
		if isNotFound(err) {
			return resp.Integer(-2)
		}
		if err != nil {
			return env.storageError(name, err)
		}
		if deadline == 0 {
			return resp.Integer(-1)
		}
		ms := deadline
		if !absolute {
			ms = max(deadline-env.nowMillis(), 0)
		}
		return resp.Integer((ms + unit/2) / unit)
	}
}

// persist is PERSIST key. It replies 1 if it removed a deadline.
func persist(env *Env, _ *Client, args [][]byte) resp.Value {
	key := args[1]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := env.Locks.Lock(key)
	defer env.Locks.Unlock(stripe)
	value, deadline, err := env.load(key)
	if isNotFound(err) || (err == nil && deadline == 0) {
		return resp.Integer(0)
	}
	if err != nil {
		return env.storageError("persist", err)
	}
	if err := env.save(key, value, 0); err != nil {
		return env.storageError("persist", err)
	}
	return resp.Integer(1)
}
