// SPDX-License-Identifier: MIT

package command

import (
	"strings"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
)

var (
	replyBadNumKeysSet = resp.Error("ERR numkeys should be greater than 0")
	replyCardKeys      = resp.Error("ERR Number of keys can't be greater than number of args")
	replyBadLimit      = resp.Error("ERR LIMIT can't be negative")
)

// smove is SMOVE source destination member. It moves a member from one set to another
// in one write, and replies 1, or 0 if the member is not in the source. A source that
// becomes empty goes away. If both keys are the same set, it only checks the member.
func smove(env *Env, cl *Client, args [][]byte) resp.Value {
	src, dst, member := args[1], args[2], args[3]
	for _, k := range [][]byte{src, dst} {
		if bad, ok := env.checkKey(k); !ok {
			return bad
		}
	}
	if bad, ok := env.checkFields([][]byte{member}); !ok {
		return bad
	}
	keys := [][]byte{src, dst}
	stripes := cl.lockKeys(env, 2, func(i int) []byte { return keys[i] })
	defer cl.unlockStripes(env, stripes)
	from, err := env.openColl(src, typeSet)
	if err != nil {
		return env.storageError("smove", err)
	}
	to, err := env.openColl(dst, typeSet)
	if err != nil {
		return env.storageError("smove", err)
	}
	_, in, err := from.get(member)
	if err != nil {
		return env.storageError("smove", err)
	}
	if !in {
		return resp.Integer(0)
	}
	if string(src) == string(dst) {
		return resp.Integer(1)
	}
	var b shale.Batch
	if from.count > 1 {
		from.del(&b, member)
	}
	from.stage(&b, from.count-1)
	_, there, err := to.get(member)
	if err != nil {
		return env.storageError("smove", err)
	}
	if !there {
		if err := to.ensureUID(); err != nil {
			return env.storageError("smove", err)
		}
		to.put(&b, member, nil)
		to.stage(&b, to.count+1)
	}
	if err := env.Store.Write(&b); err != nil {
		return env.storageError("smove", err)
	}
	return resp.Integer(1)
}

// setCard returns the handler of SINTERCARD, SUNIONCARD and SDIFFCARD: the number of
// members of the intersection, the union or the difference of sets, as of one
// snapshot. LIMIT stops the count at a number, and APPROX of SUNIONCARD is accepted
// and does nothing, because the count is exact.
func setCard(kind setOpKind) Handler {
	return func(env *Env, _ *Client, args [][]byte) resp.Value {
		name := strings.ToLower(string(args[0]))
		n, ok := parseInt(args[1])
		if !ok || n <= 0 {
			return replyBadNumKeysSet
		}
		if n > int64(len(args)-2) {
			return replyCardKeys
		}
		keys, opts := args[2:2+n], args[2+n:]
		var limit int64
		for i := 0; i < len(opts); i++ {
			switch strings.ToUpper(string(opts[i])) {
			case "LIMIT":
				if i+1 >= len(opts) {
					return replySyntax
				}
				i++
				v, ok := parseInt(opts[i])
				if !ok || v < 0 {
					return replyBadLimit
				}
				limit = v
			case "APPROX":
				if kind != opUnion {
					return replySyntax
				}
			default:
				return replySyntax
			}
		}
		for _, k := range keys {
			if bad, ok := env.checkKey(k); !ok {
				return bad
			}
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
		count, big, err := cardinality(env, snap, kind, sets, limit)
		switch {
		case err != nil:
			return env.storageError(name, err)
		case big:
			return replyReplyBig
		}
		return resp.Integer(count)
	}
}

// cardinality counts the members of the union, the intersection or the difference of
// sets, and stops at limit if it is more than zero. It reports true if the union needs
// more memory than the limit of a reply.
func cardinality(env *Env, snap *shale.Snapshot, kind setOpKind, sets []*coll, limit int64) (count int64, tooBig bool, err error) {
	var ferr error
	switch kind {
	case opUnion:
		seen := map[string]struct{}{}
		bytesSeen := 0
		err = nil
		for _, c := range sets {
			e := c.each(snap, func(m, _ []byte) bool {
				if _, dup := seen[string(m)]; dup {
					return true
				}
				bytesSeen += len(m)
				if env.MaxReply > 0 && bytesSeen > env.MaxReply {
					tooBig = true
					return false
				}
				seen[string(m)] = struct{}{}
				count++
				return limit == 0 || count < limit
			})
			if e != nil {
				return 0, false, e
			}
			if tooBig || (limit > 0 && count >= limit) {
				break
			}
		}
		return count, tooBig, nil
	case opInter:
		smallest := sets[0]
		for _, c := range sets {
			if !c.found {
				return 0, false, nil
			}
			if c.count < smallest.count {
				smallest = c
			}
		}
		err = smallest.each(snap, func(m, _ []byte) bool {
			for _, c := range sets {
				if c == smallest {
					continue
				}
				ok, e := c.hasIn(snap, m)
				if e != nil {
					ferr = e
					return false
				}
				if !ok {
					return true
				}
			}
			count++
			return limit == 0 || count < limit
		})
	default:
		err = sets[0].each(snap, func(m, _ []byte) bool {
			for _, c := range sets[1:] {
				ok, e := c.hasIn(snap, m)
				if e != nil {
					ferr = e
					return false
				}
				if ok {
					return true
				}
			}
			count++
			return limit == 0 || count < limit
		})
	}
	if err == nil {
		err = ferr
	}
	return count, false, err
}

// numKeys returns the number of keys that the argument at position at gives, if it is
// a number from 1 to the largest that the rest of the arguments can hold, and 0
// otherwise.
func numKeys(args [][]byte, at, perKey int) int {
	if len(args) <= at {
		return 0
	}
	n, ok := parseInt(args[at])
	if !ok || n <= 0 || n > int64(len(args)-at-1)/int64(perKey) {
		return 0
	}
	return int(n)
}

// cardKeys returns the keys of SINTERCARD, SUNIONCARD and SDIFFCARD.
func cardKeys(args [][]byte) [][]byte {
	n := numKeys(args, 1, 1)
	if n == 0 {
		return nil
	}
	return args[2 : 2+n]
}

// msetexKeys returns the keys of MSETEX, which are every other argument after the count.
func msetexKeys(args [][]byte) [][]byte {
	n := numKeys(args, 1, 2)
	if n == 0 {
		return nil
	}
	keys := make([][]byte, n)
	for i := range n {
		keys[i] = args[2+2*i]
	}
	return keys
}
