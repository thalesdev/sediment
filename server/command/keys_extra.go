// SPDX-License-Identifier: MIT

package command

import (
	"strconv"
	"strings"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

var (
	replyCopySame      = resp.Error("ERR source and destination objects are the same")
	replyCopyDB        = resp.Error("ERR DB index is out of range")
	replyCopyBig       = resp.Error("ERR the collection is too large for COPY")
	replyNoLFU         = resp.Error("ERR An LFU maxmemory policy is not selected, access frequency not tracked. Please note that when switching between policies at runtime LRU and LFU data will take some time to adjust.")
	replyNoIdle        = resp.Error("ERR the idle time of a key is not tracked by this server")
	replyBadCommand    = resp.Error("ERR Invalid command specified")
	replyBadCmdArgs    = resp.Error("ERR Invalid arguments specified for command")
	replyNoKeysCommand = resp.Error("ERR The command has no key arguments")
	replyCmdArgsNeeded = resp.Error("ERR wrong number of arguments for 'command|getkeys' command")
)

// maxCopyElements is the largest collection that COPY writes in one batch.
const maxCopyElements = 1 << 20

// extraKeyCommands returns COPY, TOUCH, TIME, RESET and OBJECT.
func extraKeyCommands() []*Spec {
	return []*Spec{
		{Name: "copy", Arity: -3, Categories: acl.CatWrite | acl.CatKeyspace | acl.CatSlow, FirstKey: 1, LastKey: 2, KeyStep: 1, Handler: copyCmd},
		{Name: "touch", Arity: -2, Categories: acl.CatRead | acl.CatKeyspace | acl.CatFast, FirstKey: 1, LastKey: -1, KeyStep: 1, Handler: exists},
		{Name: "time", Arity: 1, Categories: acl.CatFast, Handler: timeCmd},
		{Name: "reset", Arity: 1, Categories: acl.CatFast | acl.CatConnection, NoAuth: true, Tx: TxImmediate, Handler: reset},
		{Name: "object", Arity: -2, Categories: acl.CatSlow | acl.CatKeyspace | acl.CatRead, Subs: []string{"encoding", "freq", "idletime", "refcount", "help"}, FirstKey: 2, LastKey: 2, KeyStep: 1, Handler: object},
	}
}

// copyCmd is COPY source destination [DB db] [REPLACE]. It copies a key of any type,
// with its expiry, to a new key in one write. There is only database 0, so DB must be 0.
// It replies 1 if it copied, and 0 if the source does not exist or the destination does
// and there is no REPLACE.
func copyCmd(env *Env, cl *Client, args [][]byte) resp.Value {
	src, dst := args[1], args[2]
	replace := false
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "REPLACE":
			replace = true
		case "DB":
			if i+1 >= len(args) {
				return replySyntax
			}
			i++
			n, ok := parseInt(args[i])
			if !ok {
				return replyNotInt
			}
			if n != 0 {
				return replyCopyDB
			}
		default:
			return replySyntax
		}
	}
	for _, k := range [][]byte{src, dst} {
		if bad, ok := env.checkKey(k); !ok {
			return bad
		}
	}
	if string(src) == string(dst) {
		return replyCopySame
	}
	keys := [][]byte{src, dst}
	stripes := cl.lockKeys(env, 2, func(i int) []byte { return keys[i] })
	defer cl.unlockStripes(env, stripes)
	rec, err := env.loadRecord(src)
	if err != nil {
		if isNotFound(err) {
			return resp.Integer(0)
		}
		return env.storageError("copy", err)
	}
	old, err := env.loadRecord(dst)
	found := err == nil
	if err != nil && !isNotFound(err) {
		return env.storageError("copy", err)
	}
	if found && !replace {
		return resp.Integer(0)
	}
	var b shale.Batch
	if found {
		batchDelete(&b, dst, old)
	}
	if rec.typ == typeString {
		batchPut(&b, dst, rec.body, rec.deadline)
	} else if bad, ok := copyCollection(env, &b, src, dst, rec); !ok {
		return bad
	}
	if err := env.Store.Write(&b); err != nil {
		return env.storageError("copy", err)
	}
	return resp.Integer(1)
}

// copyCollection adds to b the writes that make dst a copy of the collection src, with
// a new id. It reads the elements as of one snapshot.
func copyCollection(env *Env, b *shale.Batch, src, dst []byte, rec record) (resp.Value, bool) {
	snap, err := env.Store.Snapshot()
	if err != nil {
		return env.storageError("copy", err), false
	}
	defer snap.Release()
	from, err := env.collAt(snap, src, rec.typ)
	if err != nil {
		return env.storageError("copy", err), false
	}
	if from.count > maxCopyElements {
		return replyCopyBig, false
	}
	uid, err := env.allocUID()
	if err != nil {
		return env.storageError("copy", err), false
	}
	to := &coll{env: env, key: dst, slot: slotOf(dst), typ: rec.typ, uid: uid}
	to.rec.deadline = rec.deadline
	var n uint64
	if err := from.each(snap, func(field, payload []byte) bool {
		to.put(b, field, payload)
		n++
		return true
	}); err != nil {
		return env.storageError("copy", err), false
	}
	to.stage(b, n)
	return resp.Value{}, true
}

// timeCmd is TIME. It replies with the seconds and the microseconds of the clock.
func timeCmd(env *Env, _ *Client, _ [][]byte) resp.Value {
	now := time.Now()
	if env.Now != nil {
		now = env.Now()
	}
	return resp.Array(
		resp.BulkString(strconv.FormatInt(now.Unix(), 10)),
		resp.BulkString(strconv.Itoa(now.Nanosecond()/1000)),
	)
}

// reset is RESET. It puts the connection back as it was when it opened: it drops a
// transaction and the watched keys, the name, the protocol and the cursors, and it
// signs the client out, so that a server with users asks for a sign in again.
func reset(env *Env, c *Client, _ [][]byte) resp.Value {
	c.resetTx()
	c.scan = scanState{}
	c.setName("")
	c.setProto(2)
	c.setUser("default")
	c.Authed = env.Access == nil || !env.Access.RequireAuth()
	return resp.Simple("RESET")
}

// objectEncoding is the name that OBJECT ENCODING gives to a record. The server has one
// representation for each type, so the name only follows the one that Redis would use
// for a value of the same kind.
func objectEncoding(r record) string {
	if r.typ != typeString {
		return "hashtable"
	}
	if n, err := strconv.ParseInt(string(r.body), 10, 64); err == nil && strconv.FormatInt(n, 10) == string(r.body) {
		return "int"
	}
	if len(r.body) <= 44 {
		return "embstr"
	}
	return "raw"
}

// object is OBJECT ENCODING | FREQ | IDLETIME | REFCOUNT key, and OBJECT HELP.
func object(env *Env, _ *Client, args [][]byte) resp.Value {
	sub := strings.ToUpper(string(args[1]))
	if sub == "HELP" {
		return resp.Array(
			resp.Simple("OBJECT <subcommand> [<arg> [value] [opt] ...]. Subcommands are:"),
			resp.Simple("ENCODING <key>"),
			resp.Simple("    Return the kind of internal representation used in order to store the value associated with a <key>."),
			resp.Simple("FREQ <key>"),
			resp.Simple("    Not tracked: the server has no LFU policy."),
			resp.Simple("IDLETIME <key>"),
			resp.Simple("    Not tracked: the server does not keep the time of the last access of a key."),
			resp.Simple("REFCOUNT <key>"),
			resp.Simple("    Return the number of references of the value associated with the specified <key>."),
			resp.Simple("HELP"),
			resp.Simple("    Print this help."),
		)
	}
	switch sub {
	case "ENCODING", "FREQ", "IDLETIME", "REFCOUNT":
	default:
		return resp.Error("ERR unknown subcommand '" + printable(args[1], 32) + "' of OBJECT")
	}
	if len(args) != 3 {
		return WrongArgs("object|" + strings.ToLower(sub))
	}
	if bad, ok := env.checkKey(args[2]); !ok {
		return bad
	}
	rec, err := env.loadRecord(args[2])
	if isNotFound(err) {
		return resp.Null()
	}
	if err != nil {
		return env.storageError("object", err)
	}
	switch sub {
	case "ENCODING":
		return resp.BulkString(objectEncoding(rec))
	case "REFCOUNT":
		return resp.Integer(1)
	case "FREQ":
		return replyNoLFU
	}
	return replyNoIdle
}

// commandKeys is COMMAND GETKEYS and COMMAND GETKEYSANDFLAGS.
func commandKeys(env *Env, args [][]byte, withFlags bool) resp.Value {
	if len(args) < 3 {
		return replyCmdArgsNeeded
	}
	spec := env.Registry.Lookup(args[2])
	if spec == nil {
		return replyBadCommand
	}
	cmd := args[2:]
	if (spec.Arity > 0 && len(cmd) != spec.Arity) || (spec.Arity < 0 && len(cmd) < -spec.Arity) {
		return replyBadCmdArgs
	}
	keys := KeysOf(spec, cmd)
	if len(keys) == 0 {
		if spec.FirstKey == 0 && spec.KeyFunc == nil {
			return replyNoKeysCommand
		}
		if spec.KeyFunc != nil {
			return replyBadCmdArgs
		}
		return resp.Array()
	}
	out := make([]resp.Value, len(keys))
	for i, k := range keys {
		if !withFlags {
			out[i] = resp.Bulk(k)
			continue
		}
		flags := keyFlags(spec, cmd)
		fl := make([]resp.Value, len(flags))
		for j, f := range flags {
			fl[j] = resp.BulkString(f)
		}
		out[i] = resp.Array(resp.Bulk(k), resp.Array(fl...))
	}
	return resp.Array(out...)
}

// keyFlags returns the flags of the keys of a command in COMMAND GETKEYSANDFLAGS:
// RO or RW for how the command uses the key, and what it does to the value.
func keyFlags(spec *Spec, cmd [][]byte) []string {
	has := func(opt string) bool {
		for _, a := range cmd[2:] {
			if strings.EqualFold(string(a), opt) {
				return true
			}
		}
		return false
	}
	switch spec.Name {
	case "set":
		switch {
		case has("GET"):
			return []string{"RW", "access", "update"}
		case has("IFEQ"), has("IFNE"), has("IFDEQ"), has("IFDNE"), has("NX"), has("XX"), has("KEEPTTL"):
			return []string{"RW", "update"}
		}
		return []string{"OW", "update"}
	case "mset", "msetex", "setex", "psetex":
		return []string{"OW", "update"}
	case "delex":
		if len(cmd) > 2 {
			return []string{"RW", "delete"}
		}
		return []string{"RM", "delete"}
	case "del", "unlink":
		return []string{"RM", "delete"}
	}
	if spec.Categories&acl.CatWrite != 0 {
		return []string{"RW", "access", "update"}
	}
	return []string{"RO", "access"}
}
