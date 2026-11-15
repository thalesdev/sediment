// SPDX-License-Identifier: MIT

package command

import (
	"bytes"
	"math"
	"strconv"
	"strings"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

var (
	replyHashNotInt   = resp.Error("ERR hash value is not an integer")
	replyHashNotFloat = resp.Error("ERR hash value is not a float")
	replyFloatInvalid = resp.Error("ERR value is not a valid float")
	replyNaN          = resp.Error("ERR increment would produce NaN or Infinity")
)

// hashCommands returns the commands of hashes.
func hashCommands() []*Spec {
	write := acl.CatWrite | acl.CatHash | acl.CatFast
	read := acl.CatRead | acl.CatHash | acl.CatFast
	one := func(name string, arity int, cats acl.Category, h Handler) *Spec {
		return &Spec{Name: name, Arity: arity, Categories: cats, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: h}
	}
	return []*Spec{
		one("hset", -4, write, hset),
		one("hmset", -4, write, hmset),
		one("hsetnx", 4, write, hsetnx),
		one("hdel", -3, write, hdel),
		one("hincrby", 4, write, hincrby),
		one("hincrbyfloat", 4, write, hincrbyfloat),
		one("hget", 3, read, hget),
		one("hmget", -3, read, hmget),
		one("hexists", 3, read, hexists),
		one("hlen", 2, read, hlen),
		one("hstrlen", 3, read, hstrlen),
		one("hgetall", 2, acl.CatRead|acl.CatHash|acl.CatSlow, hgetall),
		one("hkeys", 2, acl.CatRead|acl.CatHash|acl.CatSlow, hkeys),
		one("hvals", 2, acl.CatRead|acl.CatHash|acl.CatSlow, hvals),
		one("hscan", -3, acl.CatRead|acl.CatHash|acl.CatSlow, hscan),
	}
}

// checkFields checks the size of each field.
func (e *Env) checkFields(fields [][]byte) (resp.Value, bool) {
	for _, f := range fields {
		if bad, ok := e.checkKey(f); !ok {
			return bad, false
		}
	}
	return resp.Value{}, true
}

// hsetFields writes pairs of field and value into the hash at key and returns the
// number of fields that did not exist.
func hsetFields(env *Env, cl *Client, name string, key []byte, pairs [][]byte) (int64, resp.Value, bool) {
	fields := make([][]byte, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		fields = append(fields, pairs[i])
	}
	if bad, ok := env.checkKey(key); !ok {
		return 0, bad, false
	}
	if bad, ok := env.checkFields(fields); !ok {
		return 0, bad, false
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	c, err := env.openColl(key, typeHash)
	if err != nil {
		return 0, env.storageError(name, err), false
	}
	if err := c.ensureUID(); err != nil {
		return 0, env.storageError(name, err), false
	}
	var b shale.Batch
	added := int64(0)
	seen := make(map[string]struct{}, len(fields))
	for i := 0; i < len(pairs); i += 2 {
		field, value := pairs[i], pairs[i+1]
		if _, dup := seen[string(field)]; !dup {
			if _, exists, err := c.get(field); err != nil {
				return 0, env.storageError(name, err), false
			} else if !exists {
				added++
			}
			seen[string(field)] = struct{}{}
		}
		c.put(&b, field, value)
	}
	if err := c.finish(&b, c.count+uint64(added)); err != nil {
		return 0, env.storageError(name, err), false
	}
	return added, resp.Value{}, true
}

// hset is HSET key field value [field value ...].
func hset(env *Env, cl *Client, args [][]byte) resp.Value {
	if len(args)%2 != 0 {
		return WrongArgs("hset")
	}
	added, bad, ok := hsetFields(env, cl, "hset", args[1], args[2:])
	if !ok {
		return bad
	}
	return resp.Integer(added)
}

// hmset is HMSET key field value [field value ...].
func hmset(env *Env, cl *Client, args [][]byte) resp.Value {
	if len(args)%2 != 0 {
		return WrongArgs("hmset")
	}
	if _, bad, ok := hsetFields(env, cl, "hmset", args[1], args[2:]); !ok {
		return bad
	}
	return replyOK
}

// hsetnx is HSETNX key field value.
func hsetnx(env *Env, cl *Client, args [][]byte) resp.Value {
	key, field, value := args[1], args[2], args[3]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	if bad, ok := env.checkKey(field); !ok {
		return bad
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	c, err := env.openColl(key, typeHash)
	if err != nil {
		return env.storageError("hsetnx", err)
	}
	if _, exists, err := c.get(field); err != nil {
		return env.storageError("hsetnx", err)
	} else if exists {
		return resp.Integer(0)
	}
	if err := c.ensureUID(); err != nil {
		return env.storageError("hsetnx", err)
	}
	var b shale.Batch
	c.put(&b, field, value)
	if err := c.finish(&b, c.count+1); err != nil {
		return env.storageError("hsetnx", err)
	}
	return resp.Integer(1)
}

// hdel is HDEL key field [field ...].
func hdel(env *Env, cl *Client, args [][]byte) resp.Value {
	key, fields := args[1], args[2:]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	c, err := env.openColl(key, typeHash)
	if err != nil {
		return env.storageError("hdel", err)
	}
	if !c.found {
		return resp.Integer(0)
	}
	var doomed [][]byte
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		if _, dup := seen[string(f)]; dup {
			continue
		}
		seen[string(f)] = struct{}{}
		if _, exists, err := c.get(f); err != nil {
			return env.storageError("hdel", err)
		} else if exists {
			doomed = append(doomed, f)
		}
	}
	if len(doomed) == 0 {
		return resp.Integer(0)
	}
	removed := uint64(len(doomed))
	var b shale.Batch
	if removed < c.count {
		for _, f := range doomed {
			c.del(&b, f)
		}
	}
	if err := c.finish(&b, c.count-min(removed, c.count)); err != nil {
		return env.storageError("hdel", err)
	}
	return resp.Integer(int64(removed))
}

// hincrby is HINCRBY key field increment.
func hincrby(env *Env, cl *Client, args [][]byte) resp.Value {
	delta, ok := parseInt(args[3])
	if !ok {
		return replyNotInt
	}
	return hmodify(env, cl, "hincrby", args[1], args[2], func(old []byte, exists bool) ([]byte, resp.Value, bool) {
		var cur int64
		if exists {
			n, ok := parseInt(old)
			if !ok {
				return nil, replyHashNotInt, false
			}
			cur = n
		}
		if (delta > 0 && cur > math.MaxInt64-delta) || (delta < 0 && cur < math.MinInt64-delta) {
			return nil, replyOverflow, false
		}
		return strconv.AppendInt(nil, cur+delta, 10), resp.Value{}, true
	})
}

// hincrbyfloat is HINCRBYFLOAT key field increment.
func hincrbyfloat(env *Env, cl *Client, args [][]byte) resp.Value {
	delta, err := strconv.ParseFloat(string(args[3]), 64)
	if err != nil || math.IsNaN(delta) || math.IsInf(delta, 0) {
		return replyFloatInvalid
	}
	return hmodify(env, cl, "hincrbyfloat", args[1], args[2], func(old []byte, exists bool) ([]byte, resp.Value, bool) {
		var cur float64
		if exists {
			f, err := strconv.ParseFloat(string(old), 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, replyHashNotFloat, false
			}
			cur = f
		}
		sum := cur + delta
		if math.IsNaN(sum) || math.IsInf(sum, 0) {
			return nil, replyNaN, false
		}
		return strconv.AppendFloat(nil, sum, 'f', -1, 64), resp.Value{}, true
	})
}

// hmodify reads a field, gives its payload to change, and writes what it returns.
// It replies with the new payload as a bulk string for a float and as an integer
// for HINCRBY.
func hmodify(env *Env, cl *Client, name string, key, field []byte, change func(old []byte, exists bool) ([]byte, resp.Value, bool)) resp.Value {
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	if bad, ok := env.checkKey(field); !ok {
		return bad
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	c, err := env.openColl(key, typeHash)
	if err != nil {
		return env.storageError(name, err)
	}
	old, exists, err := c.get(field)
	if err != nil {
		return env.storageError(name, err)
	}
	next, bad, ok := change(old, exists)
	if !ok {
		return bad
	}
	if err := c.ensureUID(); err != nil {
		return env.storageError(name, err)
	}
	var b shale.Batch
	c.put(&b, field, next)
	count := c.count
	if !exists {
		count++
	}
	if err := c.finish(&b, count); err != nil {
		return env.storageError(name, err)
	}
	if name == "hincrby" {
		n, _ := parseInt(next)
		return resp.Integer(n)
	}
	return resp.Bulk(next)
}

// hgetField reads one field of a hash without a lock.
func hgetField(env *Env, name string, key, field []byte) ([]byte, bool, resp.Value, bool) {
	if bad, ok := env.checkKey(key); !ok {
		return nil, false, bad, false
	}
	if bad, ok := env.checkKey(field); !ok {
		return nil, false, bad, false
	}
	c, err := env.openColl(key, typeHash)
	if err != nil {
		return nil, false, env.storageError(name, err), false
	}
	payload, exists, err := c.get(field)
	if err != nil {
		return nil, false, env.storageError(name, err), false
	}
	return payload, exists, resp.Value{}, true
}

// hget is HGET key field.
func hget(env *Env, _ *Client, args [][]byte) resp.Value {
	payload, exists, bad, ok := hgetField(env, "hget", args[1], args[2])
	switch {
	case !ok:
		return bad
	case !exists:
		return resp.Null()
	}
	return resp.Bulk(payload)
}

// hexists is HEXISTS key field.
func hexists(env *Env, _ *Client, args [][]byte) resp.Value {
	_, exists, bad, ok := hgetField(env, "hexists", args[1], args[2])
	switch {
	case !ok:
		return bad
	case exists:
		return resp.Integer(1)
	}
	return resp.Integer(0)
}

// hstrlen is HSTRLEN key field.
func hstrlen(env *Env, _ *Client, args [][]byte) resp.Value {
	payload, exists, bad, ok := hgetField(env, "hstrlen", args[1], args[2])
	switch {
	case !ok:
		return bad
	case !exists:
		return resp.Integer(0)
	}
	return resp.Integer(int64(len(payload)))
}

// hmget is HMGET key field [field ...].
func hmget(env *Env, _ *Client, args [][]byte) resp.Value {
	key := args[1]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	if bad, ok := env.checkFields(args[2:]); !ok {
		return bad
	}
	c, err := env.openColl(key, typeHash)
	if err != nil {
		return env.storageError("hmget", err)
	}
	out := make([]resp.Value, 0, len(args)-2)
	total := 0
	for _, f := range args[2:] {
		payload, exists, err := c.get(f)
		if err != nil {
			return env.storageError("hmget", err)
		}
		if !exists {
			out = append(out, resp.Null())
			continue
		}
		total += len(payload)
		if env.MaxReply > 0 && total > env.MaxReply {
			return replyReplyBig
		}
		out = append(out, resp.Bulk(payload))
	}
	return resp.Array(out...)
}

// hlen is HLEN key.
func hlen(env *Env, _ *Client, args [][]byte) resp.Value {
	if bad, ok := env.checkKey(args[1]); !ok {
		return bad
	}
	c, err := env.openColl(args[1], typeHash)
	if err != nil {
		return env.storageError("hlen", err)
	}
	return resp.Integer(int64(c.count))
}

// hgetall is HGETALL key. The reply is a map in RESP3 and a flat array in RESP2.
func hgetall(env *Env, _ *Client, args [][]byte) resp.Value {
	return hdump(env, "hgetall", args[1], true, true)
}

// hkeys is HKEYS key.
func hkeys(env *Env, _ *Client, args [][]byte) resp.Value {
	return hdump(env, "hkeys", args[1], true, false)
}

// hvals is HVALS key.
func hvals(env *Env, _ *Client, args [][]byte) resp.Value {
	return hdump(env, "hvals", args[1], false, true)
}

// hdump reads all the fields of a hash as of one snapshot.
func hdump(env *Env, name string, key []byte, fields, values bool) resp.Value {
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	snap, c, err := env.readColl(key, typeHash)
	if err != nil {
		return env.storageError(name, err)
	}
	defer snap.Release()
	var out []resp.Value
	total := 0
	tooBig := false
	err = c.each(snap, func(field, payload []byte) bool {
		if fields {
			total += len(field)
		}
		if values {
			total += len(payload)
		}
		if env.MaxReply > 0 && total > env.MaxReply {
			tooBig = true
			return false
		}
		if fields {
			out = append(out, resp.Bulk(bytes.Clone(field)))
		}
		if values {
			out = append(out, resp.Bulk(bytes.Clone(payload)))
		}
		return true
	})
	switch {
	case err != nil:
		return env.storageError(name, err)
	case tooBig:
		return replyReplyBig
	case fields && values:
		return resp.Map(out...)
	}
	return resp.Array(out...)
}

// hscan is HSCAN key cursor [MATCH pattern] [COUNT count]. The cursor names the
// last field that a call looked at. If the hash was deleted and made again, the
// scan ends.
func hscan(env *Env, cl *Client, args [][]byte) resp.Value {
	return collScan(env, cl, args, "hscan", typeHash, true)
}

// collScan is HSCAN and SSCAN. A page has the fields, and the values if the
// collection is a hash.
func collScan(env *Env, cl *Client, args [][]byte, name string, typ valueType, withValues bool) resp.Value {
	if len(args) < 3 {
		return WrongArgs(name)
	}
	key := args[1]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	cursor, err := strconv.ParseUint(string(args[2]), 10, 64)
	if err != nil {
		return replyBadCursor
	}
	count := defaultScanCount
	pattern := []byte("*")
	opts := args[3:]
	if len(opts)%2 != 0 {
		return replySyntax
	}
	for i := 0; i < len(opts); i += 2 {
		switch strings.ToUpper(string(opts[i])) {
		case "MATCH":
			pattern = opts[i+1]
		case "COUNT":
			n, ok := parseInt(opts[i+1])
			if !ok {
				return replyNotInt
			}
			if n < 1 {
				return replySyntax
			}
			count = int(min(n, maxScanCount))
		default:
			return replySyntax
		}
	}
	var resume []byte
	if cursor != 0 {
		var ok bool
		if resume, ok = cl.scan.lookup(cursor); !ok {
			return replyBadCursor
		}
	}
	snap, c, err := env.readColl(key, typ)
	if err != nil {
		return env.storageError(name, err)
	}
	defer snap.Release()
	if !c.found || (resume != nil && !bytes.HasPrefix(resume, elementPrefix(c.slot, c.uid))) {
		return scanReply("0", nil)
	}
	it, err := snap.NewIterator(elementBounds(c.slot, c.uid))
	if err != nil {
		return env.storageError(name, err)
	}
	defer it.Close()
	if resume == nil {
		it.SeekToFirst()
	} else {
		it.Seek(resume)
		if it.Valid() && bytes.Equal(it.Key(), resume) {
			it.Next()
		}
	}
	var page []resp.Value
	var last []byte
	total, examined := 0, 0
	for it.Valid() && examined < count {
		field, err := fieldOf(it.Key())
		if err != nil {
			return env.storageError(name, err)
		}
		payload, err := decodeElement(it.Value())
		if err != nil {
			return env.storageError(name, err)
		}
		if matchAll(pattern) || globMatch(pattern, field) {
			size := len(field)
			if withValues {
				size += len(payload)
			}
			if env.MaxReply > 0 && total+size > env.MaxReply && len(page) > 0 {
				break
			}
			total += size
			page = append(page, resp.Bulk(bytes.Clone(field)))
			if withValues {
				page = append(page, resp.Bulk(bytes.Clone(payload)))
			}
		}
		last = append(last[:0], it.Key()...)
		examined++
		it.Next()
	}
	if err := it.Err(); err != nil {
		return env.storageError(name, err)
	}
	next := "0"
	if it.Valid() && last != nil {
		next = strconv.FormatUint(cl.scan.remember(last), 10)
	}
	return scanReply(next, page)
}
