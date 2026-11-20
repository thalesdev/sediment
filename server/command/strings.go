// SPDX-License-Identifier: MIT

package command

import (
	"strings"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

// stringCommands returns GET, SET, DEL, EXISTS, MGET and MSET.
func stringCommands() []*Spec {
	return []*Spec{
		{Name: "get", Arity: 2, Categories: acl.CatRead | acl.CatString | acl.CatFast, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: get},
		{Name: "set", Arity: -3, Categories: acl.CatWrite | acl.CatString | acl.CatSlow, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: set},
		{Name: "del", Arity: -2, Categories: acl.CatWrite | acl.CatKeyspace | acl.CatSlow, FirstKey: 1, LastKey: -1, KeyStep: 1, Handler: del},
		{Name: "exists", Arity: -2, Categories: acl.CatRead | acl.CatKeyspace | acl.CatFast, FirstKey: 1, LastKey: -1, KeyStep: 1, Handler: exists},
		{Name: "mget", Arity: -2, Categories: acl.CatRead | acl.CatString | acl.CatFast, FirstKey: 1, LastKey: -1, KeyStep: 1, Handler: mget},
		{Name: "mset", Arity: -3, Categories: acl.CatWrite | acl.CatString | acl.CatSlow, FirstKey: 1, LastKey: -1, KeyStep: 2, Handler: mset},
	}
}

// get is GET key.
func get(env *Env, _ *Client, args [][]byte) resp.Value {
	if bad, ok := env.checkKey(args[1]); !ok {
		return bad
	}
	value, _, err := env.load(args[1])
	if isNotFound(err) {
		return resp.Null()
	}
	if err != nil {
		return env.storageError("get", err)
	}
	return resp.Bulk(value)
}

// set is SET key value [NX | XX | IFEQ v | IFNE v | IFDEQ d | IFDNE d] [GET] [EX s |
// PX ms | EXAT s | PXAT ms | KEEPTTL].
func set(env *Env, cl *Client, args [][]byte) resp.Value {
	key, value := args[1], args[2]
	var nx, xx, wantOld, keepTTL, hasExpiry, hasCond bool
	var cond condition
	var deadline int64
	opts := args[3:]
	for i := 0; i < len(opts); i++ {
		name := strings.ToUpper(string(opts[i]))
		switch name {
		case "NX":
			nx = true
		case "XX":
			xx = true
		case "GET":
			wantOld = true
		case "IFEQ", "IFNE", "IFDEQ", "IFDNE":
			if hasCond || i+1 >= len(opts) {
				return replySyntax
			}
			cond, _ = parseCondition(name, opts[i+1])
			hasCond = true
			i++
		case "KEEPTTL":
			if hasExpiry || keepTTL {
				return replySyntax
			}
			keepTTL = true
		case "EX", "PX", "EXAT", "PXAT":
			if hasExpiry || keepTTL || i+1 >= len(opts) {
				return replySyntax
			}
			i++
			n, ok := parseInt(opts[i])
			if !ok {
				return replyNotInt
			}
			d, ok := setDeadline(env, name, n)
			if !ok {
				return resp.Error("ERR invalid expire time in 'set' command")
			}
			deadline, hasExpiry = d, true
		default:
			return replySyntax
		}
	}
	if nx && xx || hasCond && (nx || xx) {
		return replySyntax
	}
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	if !nx && !xx && !wantOld && !keepTTL && !hasCond {
		stripe := cl.rlock(env, key)
		defer cl.runlock(env, stripe)
		if err := env.save(key, value, deadline); err != nil {
			return env.storageError("set", err)
		}
		return replyOK
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	rec, err := env.loadRecord(key)
	found := err == nil
	if err != nil && !isNotFound(err) {
		return env.storageError("set", err)
	}
	if wantOld && found && rec.typ != typeString {
		return replyWrongType
	}
	if (nx && found) || (xx && !found) {
		return oldReply(wantOld, found, rec.body)
	}
	if hasCond {
		if found && rec.typ != typeString {
			return replyWrongType
		}
		if found && !cond.validDigest() {
			return replyBadDigest
		}
		if !cond.holds(found, rec.body) {
			return oldReply(wantOld, found, rec.body)
		}
	}
	if keepTTL && found {
		deadline = rec.deadline
	}
	if found && rec.typ != typeString {
		var batch shale.Batch
		batchDelete(&batch, key, rec)
		batchPut(&batch, key, value, deadline)
		err = env.Store.Write(&batch)
	} else {
		err = env.save(key, value, deadline)
	}
	if err != nil {
		return env.storageError("set", err)
	}
	if wantOld {
		return oldReply(true, found, rec.body)
	}
	return replyOK
}

// setDeadline returns the deadline of a SET option, and false if n is not
// positive or does not fit.
func setDeadline(env *Env, option string, n int64) (int64, bool) {
	if n <= 0 {
		return 0, false
	}
	switch option {
	case "EX":
		return deadlineAt(env.nowMillis(), n, 1000)
	case "PX":
		return deadlineAt(env.nowMillis(), n, 1)
	case "EXAT":
		return deadlineAt(0, n, 1000)
	default:
		return n, true
	}
}

// oldReply returns the old value for SET with GET, or null.
func oldReply(wantOld, found bool, old []byte) resp.Value {
	if wantOld && found {
		return resp.Bulk(old)
	}
	return resp.Null()
}

// del is DEL key [key ...].
func del(env *Env, cl *Client, args [][]byte) resp.Value {
	keys := args[1:]
	for _, k := range keys {
		if bad, ok := env.checkKey(k); !ok {
			return bad
		}
	}
	stripes := cl.lockKeys(env, len(keys), func(i int) []byte { return keys[i] })
	defer cl.unlockStripes(env, stripes)
	var batch shale.Batch
	var removed int64
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if _, dup := seen[string(k)]; dup {
			continue
		}
		seen[string(k)] = struct{}{}
		rec, err := env.loadRecord(k)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return env.storageError("del", err)
		}
		batchDelete(&batch, k, rec)
		removed++
	}
	if batch.Len() == 0 {
		return resp.Integer(0)
	}
	if err := env.Store.Write(&batch); err != nil {
		return env.storageError("del", err)
	}
	return resp.Integer(removed)
}

// exists is EXISTS key [key ...].
func exists(env *Env, _ *Client, args [][]byte) resp.Value {
	var n int64
	for _, k := range args[1:] {
		if bad, ok := env.checkKey(k); !ok {
			return bad
		}
		if _, err := env.loadRecord(k); err == nil {
			n++
		} else if !isNotFound(err) {
			return env.storageError("exists", err)
		}
	}
	return resp.Integer(n)
}

// mget is MGET key [key ...].
func mget(env *Env, _ *Client, args [][]byte) resp.Value {
	out := make([]resp.Value, 0, len(args)-1)
	total := 0
	for _, k := range args[1:] {
		if bad, ok := env.checkKey(k); !ok {
			return bad
		}
		rec, err := env.loadRecord(k)
		switch {
		case err == nil && rec.typ != typeString:
			out = append(out, resp.Null())
		case err == nil:
			total += len(rec.body)
			if env.MaxReply > 0 && total > env.MaxReply {
				return replyReplyBig
			}
			out = append(out, resp.Bulk(rec.body))
		case isNotFound(err):
			out = append(out, resp.Null())
		default:
			return env.storageError("mget", err)
		}
	}
	return resp.Array(out...)
}

// mset is MSET key value [key value ...]. The pairs are applied as one unit.
func mset(env *Env, cl *Client, args [][]byte) resp.Value {
	pairs := args[1:]
	if len(pairs)%2 != 0 {
		return WrongArgs("mset")
	}
	n := len(pairs) / 2
	for i := range n {
		if bad, ok := env.checkKey(pairs[2*i]); !ok {
			return bad
		}
	}
	stripes := cl.rlockKeys(env, n, func(i int) []byte { return pairs[2*i] })
	defer cl.runlockStripes(env, stripes)
	var batch shale.Batch
	for i := range n {
		batchPut(&batch, pairs[2*i], pairs[2*i+1], 0)
	}
	if err := env.Store.Write(&batch); err != nil {
		return env.storageError("mset", err)
	}
	return replyOK
}

// SetMany runs plain SET commands, each as [SET key value], as one atomic write,
// and returns the reply of each one in the same order. The server uses it for the
// commands of a pipeline that already arrived, so that they share one record of
// the log. A SET with a key that is too large gets its error and is left out of
// the write. If the write fails, every command in it gets the same error.
func SetMany(env *Env, cmds [][][]byte) []resp.Value {
	replies := make([]resp.Value, len(cmds))
	var batch shale.Batch
	included := make([]int, 0, len(cmds))
	for i, args := range cmds {
		if bad, ok := env.checkKey(args[1]); !ok {
			replies[i] = bad
			continue
		}
		batchPut(&batch, args[1], args[2], 0)
		included = append(included, i)
	}
	if batch.Len() == 0 {
		return replies
	}
	stripes := env.Locks.RLockKeys(len(included), func(n int) []byte { return cmds[included[n]][1] })
	err := env.Store.Write(&batch)
	env.Locks.RUnlockStripes(stripes)
	reply := replyOK
	if err != nil {
		reply = env.storageError("set", err)
	}
	for _, i := range included {
		replies[i] = reply
	}
	return replies
}
