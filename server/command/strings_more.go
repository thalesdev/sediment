// SPDX-License-Identifier: MIT

package command

import (
	"math"
	"strconv"
	"strings"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

var (
	replyNotFloat   = resp.Error("ERR value is not a valid float")
	replyNaNOrInf   = resp.Error("ERR increment would produce NaN or Infinity")
	replyBadOffset  = resp.Error("ERR offset is out of range")
	replyNoSuchKey  = resp.Error("ERR no such key")
	replyRenameBig  = resp.Error("ERR the collection is too large to rename into another slot")
	replyBadExpiry  = "ERR invalid expire time in '%s' command"
	maxRenameCopied = 64 << 20
)

// moreStringCommands returns SETEX, PSETEX, MSETNX, GETEX, GETRANGE, SETRANGE,
// INCRBYFLOAT, UNLINK, RENAME and RENAMENX.
func moreStringCommands() []*Spec {
	str := acl.CatWrite | acl.CatString
	return []*Spec{
		{Name: "setex", Arity: 4, Categories: str | acl.CatSlow, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: setexHandler("setex", "EX")},
		{Name: "psetex", Arity: 4, Categories: str | acl.CatSlow, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: setexHandler("psetex", "PX")},
		{Name: "msetnx", Arity: -3, Categories: str | acl.CatSlow, FirstKey: 1, LastKey: -1, KeyStep: 2, Handler: msetnx},
		{Name: "getex", Arity: -2, Categories: str | acl.CatFast, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: getex},
		{Name: "getrange", Arity: 4, Categories: acl.CatRead | acl.CatString | acl.CatSlow, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: getrange},
		{Name: "setrange", Arity: 4, Categories: str | acl.CatSlow, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: setrange},
		{Name: "incrbyfloat", Arity: 3, Categories: str | acl.CatFast, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: incrbyfloat},
		{Name: "unlink", Arity: -2, Categories: acl.CatWrite | acl.CatKeyspace | acl.CatFast, FirstKey: 1, LastKey: -1, KeyStep: 1, Handler: del},
		{Name: "rename", Arity: 3, Categories: acl.CatWrite | acl.CatKeyspace | acl.CatSlow, FirstKey: 1, LastKey: 2, KeyStep: 1, Handler: renameHandler(false)},
		{Name: "renamenx", Arity: 3, Categories: acl.CatWrite | acl.CatKeyspace | acl.CatSlow, FirstKey: 1, LastKey: 2, KeyStep: 1, Handler: renameHandler(true)},
	}
}

// setexHandler returns the handler of SETEX and PSETEX: a SET with a time.
func setexHandler(name, option string) Handler {
	return func(env *Env, cl *Client, args [][]byte) resp.Value {
		key, value := args[1], args[3]
		n, ok := parseInt(args[2])
		if !ok {
			return replyNotInt
		}
		deadline, ok := setDeadline(env, option, n)
		if !ok {
			return resp.Error(strings.Replace(replyBadExpiry, "%s", name, 1))
		}
		if bad, ok := env.checkKey(key); !ok {
			return bad
		}
		stripe := cl.rlock(env, key)
		defer cl.runlock(env, stripe)
		if err := env.save(key, value, deadline); err != nil {
			return env.storageError(name, err)
		}
		return replyOK
	}
}

// msetnx is MSETNX key value [key value ...]. It sets all the keys if none of them
// exists, and none otherwise, and it replies 1 or 0.
func msetnx(env *Env, cl *Client, args [][]byte) resp.Value {
	pairs := args[1:]
	if len(pairs)%2 != 0 {
		return WrongArgs("msetnx")
	}
	n := len(pairs) / 2
	for i := range n {
		if bad, ok := env.checkKey(pairs[2*i]); !ok {
			return bad
		}
	}
	stripes := cl.lockKeys(env, n, func(i int) []byte { return pairs[2*i] })
	defer cl.unlockStripes(env, stripes)
	for i := range n {
		if _, err := env.loadRecord(pairs[2*i]); err == nil {
			return resp.Integer(0)
		} else if !isNotFound(err) {
			return env.storageError("msetnx", err)
		}
	}
	var batch shale.Batch
	for i := range n {
		batchPut(&batch, pairs[2*i], pairs[2*i+1], 0)
	}
	if err := env.Store.Write(&batch); err != nil {
		return env.storageError("msetnx", err)
	}
	return resp.Integer(1)
}

// getex is GETEX key [EX s | PX ms | EXAT s | PXAT ms | PERSIST]. It replies with
// the value and changes the time of the key.
func getex(env *Env, cl *Client, args [][]byte) resp.Value {
	key := args[1]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	opts := args[2:]
	if len(opts) == 0 {
		value, _, err := env.load(key)
		if isNotFound(err) {
			return resp.Null()
		}
		if err != nil {
			return env.storageError("getex", err)
		}
		return resp.Bulk(value)
	}
	option := strings.ToUpper(string(opts[0]))
	var deadline int64
	var remove bool
	switch option {
	case "PERSIST":
		if len(opts) != 1 {
			return replySyntax
		}
	case "EX", "PX", "EXAT", "PXAT":
		if len(opts) != 2 {
			return replySyntax
		}
		n, ok := parseInt(opts[1])
		if !ok {
			return replyNotInt
		}
		if n <= 0 {
			return resp.Error(strings.Replace(replyBadExpiry, "%s", "getex", 1))
		}
		var fits bool
		if deadline, fits = setDeadline(env, option, n); !fits {
			return resp.Error(strings.Replace(replyBadExpiry, "%s", "getex", 1))
		}
		remove = deadline <= env.nowMillis()
	default:
		return replySyntax
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	value, _, err := env.load(key)
	if isNotFound(err) {
		return resp.Null()
	}
	if err != nil {
		return env.storageError("getex", err)
	}
	switch {
	case remove:
		err = env.Store.Delete(keyOf(key))
	default:
		err = env.save(key, value, deadline)
	}
	if err != nil {
		return env.storageError("getex", err)
	}
	return resp.Bulk(value)
}

// getrange is GETRANGE key start end. Both ends are in the range, and a negative
// one counts from the end of the string.
func getrange(env *Env, _ *Client, args [][]byte) resp.Value {
	start, ok1 := parseInt(args[2])
	end, ok2 := parseInt(args[3])
	if !ok1 || !ok2 {
		return replyNotInt
	}
	if bad, ok := env.checkKey(args[1]); !ok {
		return bad
	}
	value, _, err := env.load(args[1])
	if isNotFound(err) {
		return resp.BulkString("")
	}
	if err != nil {
		return env.storageError("getrange", err)
	}
	n := int64(len(value))
	if (start < 0 && end < 0 && start > end) || n == 0 {
		return resp.BulkString("")
	}
	if start < 0 {
		start += n
	}
	if end < 0 {
		end += n
	}
	start, end = max(start, 0), max(end, 0)
	end = min(end, n-1)
	if start > end {
		return resp.BulkString("")
	}
	return resp.Bulk(value[start : end+1])
}

// setrange is SETRANGE key offset value. It writes the value at the offset, and
// pads the string with zero bytes if the offset is past its end.
func setrange(env *Env, cl *Client, args [][]byte) resp.Value {
	key, patch := args[1], args[3]
	offset, ok := parseInt(args[2])
	if !ok {
		return replyNotInt
	}
	if offset < 0 {
		return replyBadOffset
	}
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	if offset > int64(maxStringBytes) || offset+int64(len(patch)) > int64(maxStringBytes) {
		return replyValueBig
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	old, deadline, err := env.load(key)
	if err != nil && !isNotFound(err) {
		return env.storageError("setrange", err)
	}
	if len(patch) == 0 {
		return resp.Integer(int64(len(old)))
	}
	size := max(len(old), toInt(offset)+len(patch))
	value := make([]byte, size)
	copy(value, old)
	copy(value[offset:], patch)
	if err := env.save(key, value, deadline); err != nil {
		return env.storageError("setrange", err)
	}
	return resp.Integer(int64(size))
}

// incrbyfloat is INCRBYFLOAT key increment.
func incrbyfloat(env *Env, cl *Client, args [][]byte) resp.Value {
	key := args[1]
	delta, err := strconv.ParseFloat(string(args[2]), 64)
	if err != nil || math.IsNaN(delta) || math.IsInf(delta, 0) {
		return replyNotFloat
	}
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	var cur float64
	old, deadline, err := env.load(key)
	switch {
	case err == nil:
		f, perr := strconv.ParseFloat(string(old), 64)
		if perr != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return replyNotFloat
		}
		cur = f
	case !isNotFound(err):
		return env.storageError("incrbyfloat", err)
	}
	sum := cur + delta
	if math.IsNaN(sum) || math.IsInf(sum, 0) {
		return replyNaNOrInf
	}
	out := strconv.AppendFloat(nil, sum, 'f', -1, 64)
	if err := env.save(key, out, deadline); err != nil {
		return env.storageError("incrbyfloat", err)
	}
	return resp.Bulk(out)
}

// renameHandler returns the handler of RENAME and RENAMENX.
func renameHandler(nx bool) Handler {
	return func(env *Env, cl *Client, args [][]byte) resp.Value {
		src, dst := args[1], args[2]
		name := "rename"
		if nx {
			name = "renamenx"
		}
		for _, k := range [][]byte{src, dst} {
			if bad, ok := env.checkKey(k); !ok {
				return bad
			}
		}
		keys := [][]byte{src, dst}
		stripes := cl.lockKeys(env, 2, func(i int) []byte { return keys[i] })
		defer cl.unlockStripes(env, stripes)
		rec, err := env.loadRecord(src)
		if isNotFound(err) {
			return replyNoSuchKey
		}
		if err != nil {
			return env.storageError(name, err)
		}
		if string(src) == string(dst) {
			if nx {
				return resp.Integer(0)
			}
			return replyOK
		}
		old, err := env.loadRecord(dst)
		exists := err == nil
		if err != nil && !isNotFound(err) {
			return env.storageError(name, err)
		}
		if nx && exists {
			return resp.Integer(0)
		}
		var b shale.Batch
		if exists {
			batchDelete(&b, dst, old)
		}
		if bad, ok := renameInto(env, name, &b, src, dst, rec); !ok {
			return bad
		}
		if err := env.Store.Write(&b); err != nil {
			return env.storageError(name, err)
		}
		if nx {
			return resp.Integer(1)
		}
		return replyOK
	}
}

// renameInto adds to b the move of the record rec from src to dst. A string moves
// with a put and a delete. A collection in the same slot keeps its elements, and
// only its KEY and OWNER records move. A collection that goes into another slot
// has its elements copied under a new id, because the slot is part of their keys.
func renameInto(env *Env, name string, b *shale.Batch, src, dst []byte, rec record) (resp.Value, bool) {
	if rec.typ == typeString {
		b.Delete(keyOf(src))
		b.Put(keyOf(dst), encodeRecord(typeString, rec.deadline, rec.body))
		return resp.Value{}, true
	}
	uid, count := rec.collection()
	srcSlot, dstSlot := slotOf(src), slotOf(dst)
	if srcSlot == dstSlot {
		b.Delete(keyOf(src))
		b.Put(keyOf(dst), encodeRecord(rec.typ, rec.deadline, rec.body))
		b.Put(ownerKey(dstSlot, uid), dst)
		return resp.Value{}, true
	}
	newUID, err := env.allocUID()
	if err != nil {
		return env.storageError(name, err), false
	}
	snap, c, err := env.readColl(src, rec.typ)
	if err != nil {
		return env.storageError(name, err), false
	}
	defer snap.Release()
	total := 0
	tooBig := false
	err = c.each(snap, func(field, payload []byte) bool {
		total += len(field) + len(payload)
		if total > maxRenameCopied {
			tooBig = true
			return false
		}
		b.Put(elementKey(dstSlot, newUID, subField, field), elementValue(payload))
		return true
	})
	switch {
	case err != nil:
		return env.storageError(name, err), false
	case tooBig:
		return replyRenameBig, false
	}
	batchDelete(b, src, rec)
	b.Put(keyOf(dst), encodeRecord(rec.typ, rec.deadline, collectionBody(newUID, count)))
	b.Put(ownerKey(dstSlot, newUID), dst)
	return resp.Value{}, true
}
