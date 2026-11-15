// SPDX-License-Identifier: MIT

package command

import (
	"bytes"
	"math/rand/v2"
	"slices"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

var (
	replyOutOfRange = resp.Error("ERR value is out of range, must be positive")
	replyStoreBig   = resp.Error("ERR result is too large")
	replySetBig     = resp.Error("ERR the set is too large for SRANDMEMBER with a negative count")
)

// maxRandomSet is the largest set that SRANDMEMBER with a negative count reads
// into memory.
const maxRandomSet = 100000

// setCommands returns the commands of sets.
func setCommands() []*Spec {
	write := acl.CatWrite | acl.CatSet | acl.CatFast
	read := acl.CatRead | acl.CatSet | acl.CatFast
	slow := acl.CatRead | acl.CatSet | acl.CatSlow
	one := func(name string, arity int, cats acl.Category, h Handler) *Spec {
		return &Spec{Name: name, Arity: arity, Categories: cats, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: h}
	}
	many := func(name string, arity int, cats acl.Category, first int, h Handler) *Spec {
		return &Spec{Name: name, Arity: arity, Categories: cats, FirstKey: first, LastKey: -1, KeyStep: 1, Handler: h}
	}
	return []*Spec{
		one("sadd", -3, write, sadd),
		one("srem", -3, write, srem),
		one("spop", -2, write, spop),
		one("sismember", 3, read, sismember),
		one("smismember", -3, read, smismember),
		one("scard", 2, read, scard),
		one("smembers", 2, slow, smembers),
		one("srandmember", -2, slow, srandmember),
		one("sscan", -3, slow, sscan),
		many("sunion", -2, slow, 1, setOp(opUnion, false)),
		many("sinter", -2, slow, 1, setOp(opInter, false)),
		many("sdiff", -2, slow, 1, setOp(opDiff, false)),
		many("sunionstore", -3, acl.CatWrite|acl.CatSet|acl.CatSlow, 1, setOp(opUnion, true)),
		many("sinterstore", -3, acl.CatWrite|acl.CatSet|acl.CatSlow, 1, setOp(opInter, true)),
		many("sdiffstore", -3, acl.CatWrite|acl.CatSet|acl.CatSlow, 1, setOp(opDiff, true)),
	}
}

// setReply returns a list of members: a set in RESP3 and an array in RESP2.
func setReply(members [][]byte) resp.Value {
	elems := make([]resp.Value, len(members))
	for i, m := range members {
		elems[i] = resp.Bulk(m)
	}
	return resp.Value{Kind: resp.KindSet, Elems: elems}
}

// sadd is SADD key member [member ...].
func sadd(env *Env, cl *Client, args [][]byte) resp.Value {
	key, members := args[1], args[2:]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	if bad, ok := env.checkFields(members); !ok {
		return bad
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	c, err := env.openColl(key, typeSet)
	if err != nil {
		return env.storageError("sadd", err)
	}
	if err := c.ensureUID(); err != nil {
		return env.storageError("sadd", err)
	}
	var b shale.Batch
	added := int64(0)
	seen := make(map[string]struct{}, len(members))
	for _, m := range members {
		if _, dup := seen[string(m)]; dup {
			continue
		}
		seen[string(m)] = struct{}{}
		if _, exists, err := c.get(m); err != nil {
			return env.storageError("sadd", err)
		} else if !exists {
			added++
			c.put(&b, m, nil)
		}
	}
	if added == 0 {
		return resp.Integer(0)
	}
	if err := c.finish(&b, c.count+uint64(added)); err != nil {
		return env.storageError("sadd", err)
	}
	return resp.Integer(added)
}

// removeMembers deletes the members from the set that c holds, and returns how
// many were in it. The caller holds the lock of the key.
func removeMembers(env *Env, name string, c *coll, members [][]byte) (int64, resp.Value, bool) {
	var doomed [][]byte
	seen := make(map[string]struct{}, len(members))
	for _, m := range members {
		if _, dup := seen[string(m)]; dup {
			continue
		}
		seen[string(m)] = struct{}{}
		if _, exists, err := c.get(m); err != nil {
			return 0, env.storageError(name, err), false
		} else if exists {
			doomed = append(doomed, m)
		}
	}
	if len(doomed) == 0 {
		return 0, resp.Value{}, true
	}
	removed := uint64(len(doomed))
	var b shale.Batch
	if removed < c.count {
		for _, m := range doomed {
			c.del(&b, m)
		}
	}
	if err := c.finish(&b, c.count-min(removed, c.count)); err != nil {
		return 0, env.storageError(name, err), false
	}
	return int64(removed), resp.Value{}, true
}

// srem is SREM key member [member ...].
func srem(env *Env, cl *Client, args [][]byte) resp.Value {
	key := args[1]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	c, err := env.openColl(key, typeSet)
	if err != nil {
		return env.storageError("srem", err)
	}
	if !c.found {
		return resp.Integer(0)
	}
	removed, bad, ok := removeMembers(env, "srem", c, args[2:])
	if !ok {
		return bad
	}
	return resp.Integer(removed)
}

// sismember is SISMEMBER key member.
func sismember(env *Env, _ *Client, args [][]byte) resp.Value {
	got := smembership(env, "sismember", args[1], args[2:])
	if got.Kind == resp.KindError {
		return got
	}
	return got.Elems[0]
}

// smismember is SMISMEMBER key member [member ...].
func smismember(env *Env, _ *Client, args [][]byte) resp.Value {
	return smembership(env, "smismember", args[1], args[2:])
}

// smembership replies with 1 or 0 for each member.
func smembership(env *Env, name string, key []byte, members [][]byte) resp.Value {
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	if bad, ok := env.checkFields(members); !ok {
		return bad
	}
	c, err := env.openColl(key, typeSet)
	if err != nil {
		return env.storageError(name, err)
	}
	out := make([]resp.Value, len(members))
	for i, m := range members {
		_, exists, err := c.get(m)
		if err != nil {
			return env.storageError(name, err)
		}
		out[i] = resp.Integer(0)
		if exists {
			out[i] = resp.Integer(1)
		}
	}
	return resp.Array(out...)
}

// scard is SCARD key.
func scard(env *Env, _ *Client, args [][]byte) resp.Value {
	if bad, ok := env.checkKey(args[1]); !ok {
		return bad
	}
	c, err := env.openColl(args[1], typeSet)
	if err != nil {
		return env.storageError("scard", err)
	}
	return resp.Integer(int64(c.count))
}

// members reads all the members of the set that c holds, as of the snapshot, in
// order. It returns false if the bytes pass limit, and then the members are not
// complete.
func (c *coll) members(snap *shale.Snapshot, limit int) ([][]byte, bool, error) {
	var out [][]byte
	total := 0
	complete := true
	err := c.each(snap, func(field, _ []byte) bool {
		total += len(field)
		if limit > 0 && total > limit {
			complete = false
			return false
		}
		out = append(out, bytes.Clone(field))
		return true
	})
	return out, complete, err
}

// smembers is SMEMBERS key.
func smembers(env *Env, _ *Client, args [][]byte) resp.Value {
	if bad, ok := env.checkKey(args[1]); !ok {
		return bad
	}
	snap, c, err := env.readColl(args[1], typeSet)
	if err != nil {
		return env.storageError("smembers", err)
	}
	defer snap.Release()
	members, complete, err := c.members(snap, env.MaxReply)
	switch {
	case err != nil:
		return env.storageError("smembers", err)
	case !complete:
		return replyReplyBig
	}
	return setReply(members)
}

// sscan is SSCAN key cursor [MATCH pattern] [COUNT count].
func sscan(env *Env, cl *Client, args [][]byte) resp.Value {
	return collScan(env, cl, args, "sscan", typeSet, false)
}

// parseCount reads the optional count of SPOP and SRANDMEMBER.
func parseCount(args [][]byte) (n int64, given bool, bad resp.Value, ok bool) {
	switch len(args) {
	case 0:
		return 0, false, resp.Value{}, true
	case 1:
		n, ok := parseInt(args[0])
		if !ok {
			return 0, false, replyNotInt, false
		}
		return n, true, resp.Value{}, true
	}
	return 0, false, replySyntax, false
}

// sample picks up to n members at random from the set as of the snapshot, with
// one pass and no member twice.
func (c *coll) sample(snap *shale.Snapshot, n int) ([][]byte, error) {
	out := make([][]byte, 0, min(n, int(c.count)))
	seen := 0
	err := c.each(snap, func(field, _ []byte) bool {
		seen++
		switch {
		case len(out) < n:
			out = append(out, bytes.Clone(field))
		default:
			if j := rand.IntN(seen); j < n {
				out[j] = bytes.Clone(field)
			}
		}
		return true
	})
	return out, err
}

// spop is SPOP key [count].
func spop(env *Env, cl *Client, args [][]byte) resp.Value {
	key := args[1]
	n, given, bad, ok := parseCount(args[2:])
	if !ok {
		return bad
	}
	if given && n < 0 {
		return replyOutOfRange
	}
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	snap, c, err := env.readColl(key, typeSet)
	if err != nil {
		return env.storageError("spop", err)
	}
	defer snap.Release()
	want := 1
	if given {
		want = int(min(n, int64(c.count)))
	}
	if !c.found || want == 0 {
		if given {
			return setReply(nil)
		}
		return resp.Null()
	}
	picked, err := c.sample(snap, want)
	if err != nil {
		return env.storageError("spop", err)
	}
	if _, bad, ok := removeMembers(env, "spop", c, picked); !ok {
		return bad
	}
	if !given {
		return resp.Bulk(picked[0])
	}
	slices.SortFunc(picked, bytes.Compare)
	return setReply(picked)
}

// srandmember is SRANDMEMBER key [count]. A positive count gives that many
// different members. A negative count gives that many members, and a member can
// come more than once.
func srandmember(env *Env, _ *Client, args [][]byte) resp.Value {
	key := args[1]
	n, given, bad, ok := parseCount(args[2:])
	if !ok {
		return bad
	}
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	snap, c, err := env.readColl(key, typeSet)
	if err != nil {
		return env.storageError("srandmember", err)
	}
	defer snap.Release()
	if !c.found || (given && n == 0) {
		if given {
			return resp.Array()
		}
		return resp.Null()
	}
	switch {
	case !given:
		picked, err := c.sample(snap, 1)
		if err != nil {
			return env.storageError("srandmember", err)
		}
		return resp.Bulk(picked[0])
	case n > 0:
		picked, err := c.sample(snap, int(min(n, int64(c.count))))
		if err != nil {
			return env.storageError("srandmember", err)
		}
		return arrayOf(picked)
	}
	if c.count > maxRandomSet {
		return replySetBig
	}
	all, complete, err := c.members(snap, env.MaxReply)
	switch {
	case err != nil:
		return env.storageError("srandmember", err)
	case !complete || len(all) == 0:
		return replyReplyBig
	}
	times := int(min(-n, int64(env.MaxReply)))
	out := make([]resp.Value, 0, min(times, 1<<16))
	total := 0
	for range times {
		m := all[rand.IntN(len(all))]
		total += len(m)
		if env.MaxReply > 0 && total > env.MaxReply {
			return replyReplyBig
		}
		out = append(out, resp.Bulk(m))
	}
	return resp.Array(out...)
}

// arrayOf returns an array of bulk strings.
func arrayOf(items [][]byte) resp.Value {
	out := make([]resp.Value, len(items))
	for i, it := range items {
		out[i] = resp.Bulk(it)
	}
	return resp.Array(out...)
}

type setOpKind int

const (
	opUnion setOpKind = iota
	opInter
	opDiff
)

// setOp returns the handler of SUNION, SINTER and SDIFF, and of the versions that
// store the result. All the sets are read as of one snapshot.
func setOp(kind setOpKind, store bool) Handler {
	return func(env *Env, cl *Client, args [][]byte) resp.Value {
		name := string(bytes.ToLower(args[0]))
		var dest []byte
		keys := args[1:]
		if store {
			dest, keys = args[1], args[2:]
			if bad, ok := env.checkKey(dest); !ok {
				return bad
			}
		}
		for _, k := range keys {
			if bad, ok := env.checkKey(k); !ok {
				return bad
			}
		}
		if store {
			stripe := cl.lock(env, dest)
			defer cl.unlock(env, stripe)
		}
		snap, err := env.Store.Snapshot()
		if err != nil {
			return env.storageError(name, err)
		}
		defer snap.Release()
		sets := make([]*coll, len(keys))
		for i, k := range keys {
			if sets[i], err = env.collAt(snap, k, typeSet); err != nil {
				return env.storageError(name, err)
			}
		}
		limit := env.MaxReply
		members, complete, err := combine(snap, kind, sets, limit)
		switch {
		case err != nil:
			return env.storageError(name, err)
		case !complete && store:
			return replyStoreBig
		case !complete:
			return replyReplyBig
		}
		if !store {
			return setReply(members)
		}
		return storeSet(env, name, dest, members)
	}
}

// combine computes a union, an intersection or a difference of sets as of the
// snapshot. The members come back in order. It reports false if the result passes
// limit bytes.
func combine(snap *shale.Snapshot, kind setOpKind, sets []*coll, limit int) ([][]byte, bool, error) {
	var out [][]byte
	total := 0
	add := func(m []byte) bool {
		total += len(m)
		if limit > 0 && total > limit {
			return false
		}
		out = append(out, bytes.Clone(m))
		return true
	}
	complete := true
	switch kind {
	case opUnion:
		seen := map[string]struct{}{}
		for _, c := range sets {
			err := c.each(snap, func(m, _ []byte) bool {
				if _, dup := seen[string(m)]; dup {
					return true
				}
				seen[string(m)] = struct{}{}
				complete = add(m)
				return complete
			})
			if err != nil || !complete {
				return out, complete, err
			}
		}
		slices.SortFunc(out, bytes.Compare)
		return out, true, nil
	case opInter:
		smallest := sets[0]
		for _, c := range sets {
			if !c.found {
				return nil, true, nil
			}
			if c.count < smallest.count {
				smallest = c
			}
		}
		var ferr error
		err := smallest.each(snap, func(m, _ []byte) bool {
			for _, c := range sets {
				if c == smallest {
					continue
				}
				ok, err := c.hasIn(snap, m)
				if err != nil {
					ferr = err
					return false
				}
				if !ok {
					return true
				}
			}
			complete = add(m)
			return complete
		})
		if err == nil {
			err = ferr
		}
		return out, complete, err
	default:
		var ferr error
		err := sets[0].each(snap, func(m, _ []byte) bool {
			for _, c := range sets[1:] {
				ok, err := c.hasIn(snap, m)
				if err != nil {
					ferr = err
					return false
				}
				if ok {
					return true
				}
			}
			complete = add(m)
			return complete
		})
		if err == nil {
			err = ferr
		}
		return out, complete, err
	}
}

// storeSet replaces the key dest with a set of the members, in one write, and
// replies with the number of members. An empty result deletes dest. The caller
// holds the lock of dest.
func storeSet(env *Env, name string, dest []byte, members [][]byte) resp.Value {
	var b shale.Batch
	old, err := env.loadRecord(dest)
	switch {
	case err == nil:
		batchDelete(&b, dest, old)
	case !isNotFound(err):
		return env.storageError(name, err)
	}
	if len(members) > 0 {
		uid, err := env.allocUID()
		if err != nil {
			return env.storageError(name, err)
		}
		slot := slotOf(dest)
		for _, m := range members {
			b.Put(elementKey(slot, uid, subField, m), elementValue(nil))
		}
		b.Put(keyOf(dest), encodeRecord(typeSet, 0, collectionBody(uid, uint64(len(members)))))
		b.Put(ownerKey(slot, uid), dest)
	}
	if b.Len() > 0 {
		if err := env.Store.Write(&b); err != nil {
			return env.storageError(name, err)
		}
	}
	return resp.Integer(int64(len(members)))
}
