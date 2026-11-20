// SPDX-License-Identifier: MIT

package command

import (
	"strings"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

// flushCommands returns FLUSHALL and FLUSHDB.
func flushCommands() []*Spec {
	cats := acl.CatWrite | acl.CatKeyspace | acl.CatSlow | acl.CatDangerous
	return []*Spec{
		{Name: "flushall", Arity: -1, Categories: cats, WritesAllKeys: true, Tx: TxNoQueue, Handler: flush},
		{Name: "flushdb", Arity: -1, Categories: cats, WritesAllKeys: true, Tx: TxNoQueue, Handler: flush},
	}
}

// flush is FLUSHALL [ASYNC | SYNC] and FLUSHDB [ASYNC | SYNC]. The server has only
// database 0, so the two are the same. It deletes every key of every type in one
// atomic write, with a range delete for each class of engine key, and keeps the
// SYSTEM records, which hold the format of the data. The option is accepted and
// does not change anything, because the work is one write and the engine frees the
// space in a compaction. It holds every lock, so no other write runs while it does,
// and then it adds one to every count that WATCH reads.
func flush(env *Env, c *Client, args [][]byte) resp.Value {
	if len(args) > 2 {
		return replySyntax
	}
	if len(args) == 2 {
		switch strings.ToUpper(string(args[1])) {
		case "ASYNC", "SYNC":
		default:
			return replySyntax
		}
	}
	env.Locks.LockAll()
	defer env.Locks.UnlockAll()
	var b shale.Batch
	for _, class := range []byte{classKey, classElement, classOwner} {
		b.DeleteRange([]byte{class}, []byte{class + 1})
	}
	if err := env.Store.Write(&b); err != nil {
		return env.storageError("flush", err)
	}
	env.Versions.BumpAll()
	return replyOK
}
