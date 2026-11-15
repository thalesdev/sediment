// SPDX-License-Identifier: MIT

package command

import (
	"fmt"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

// TxMode says how a command behaves between MULTI and EXEC.
type TxMode uint8

const (
	// TxQueue is the default: the command is queued and runs at EXEC.
	TxQueue TxMode = iota

	// TxImmediate commands run at once, even between MULTI and EXEC.
	TxImmediate

	// TxNoQueue commands are refused between MULTI and EXEC, and the transaction
	// is discarded at EXEC. They change the connection in a way that EXEC cannot
	// take back, such as AUTH.
	TxNoQueue
)

var (
	replyNotInMulti  = resp.Error("ERR EXEC without MULTI")
	replyNoDiscard   = resp.Error("ERR DISCARD without MULTI")
	replyNested      = resp.Error("ERR MULTI calls can not be nested")
	replyWatchMulti  = resp.Error("ERR WATCH inside MULTI is not allowed")
	replyExecAbort   = resp.Error("EXECABORT Transaction discarded because of previous errors.")
	replyTxNoAllowed = resp.Error("ERR Command not allowed inside a transaction")
	replyTxFull      = resp.Error("ERR transaction queue is full")
	replyTooManyKeys = resp.Error("ERR too many watched keys")
	replyQueued      = resp.Simple("QUEUED")
)

// queuedCmd is a command that waits for EXEC.
type queuedCmd struct {
	spec *Spec
	args [][]byte
}

// watchEntry is a key that the client watches, with what it was when WATCH ran.
type watchEntry struct {
	key      []byte
	version  uint64
	live     bool
	deadline int64
}

// txState is the transaction of a connection. Only the goroutine of the
// connection reads and changes it.
type txState struct {
	inMulti bool
	dirty   bool
	queue   []queuedCmd
	bytes   int
	watched []watchEntry
	held    bool
}

// InMulti tells if the client is between MULTI and EXEC.
func (c *Client) InMulti() bool {
	return c.tx.inMulti
}

// MarkTxDirty makes the next EXEC abort. The server calls it when a command that
// was meant for the queue fails its checks, as an unknown command does. It does
// nothing outside MULTI.
func (c *Client) MarkTxDirty() {
	if c.tx.inMulti {
		c.tx.dirty = true
	}
}

func (c *Client) resetTx() {
	c.tx.inMulti, c.tx.dirty = false, false
	c.tx.queue, c.tx.bytes, c.tx.watched = nil, 0, nil
}

// lock and the methods that follow take the lock of a key for a handler, unless
// the client is inside EXEC, which holds the locks of all its keys already. The
// locks are not reentrant, so a handler must always use these and never take a
// lock of env.Locks itself.
func (c *Client) lock(env *Env, key []byte) int {
	if c.tx.held {
		return -1
	}
	return env.Locks.Lock(key)
}

func (c *Client) unlock(env *Env, stripe int) {
	if stripe >= 0 {
		env.Locks.Unlock(stripe)
	}
}

func (c *Client) rlock(env *Env, key []byte) int {
	if c.tx.held {
		return -1
	}
	return env.Locks.RLock(key)
}

func (c *Client) runlock(env *Env, stripe int) {
	if stripe >= 0 {
		env.Locks.RUnlock(stripe)
	}
}

func (c *Client) lockKeys(env *Env, n int, key func(i int) []byte) []int {
	if c.tx.held {
		return nil
	}
	return env.Locks.LockKeys(n, key)
}

func (c *Client) unlockStripes(env *Env, stripes []int) {
	env.Locks.UnlockStripes(stripes)
}

func (c *Client) rlockKeys(env *Env, n int, key func(i int) []byte) []int {
	if c.tx.held {
		return nil
	}
	return env.Locks.RLockKeys(n, key)
}

func (c *Client) runlockStripes(env *Env, stripes []int) {
	env.Locks.RUnlockStripes(stripes)
}

// KeysOf returns the keys of a command, from the positions in its Spec.
func KeysOf(spec *Spec, args [][]byte) [][]byte {
	if spec.FirstKey <= 0 {
		return nil
	}
	last := spec.LastKey
	if last < 0 {
		last = len(args) + last
	}
	step := max(spec.KeyStep, 1)
	var keys [][]byte
	for i := spec.FirstKey; i <= last && i < len(args); i += step {
		keys = append(keys, args[i])
	}
	return keys
}

// Enqueue adds a command to the transaction of the client and replies QUEUED. It
// refuses the command, and marks the transaction to abort, if the queue has too
// many commands or bytes.
func Enqueue(env *Env, c *Client, spec *Spec, args [][]byte) resp.Value {
	size := 0
	for _, a := range args {
		size += len(a)
	}
	if (env.MaxTxCommands > 0 && len(c.tx.queue) >= env.MaxTxCommands) || (env.MaxTxBytes > 0 && c.tx.bytes+size > env.MaxTxBytes) {
		c.tx.dirty = true
		return replyTxFull
	}
	c.tx.queue = append(c.tx.queue, queuedCmd{spec: spec, args: args})
	c.tx.bytes += size
	return replyQueued
}

// RefuseInTx marks the transaction to abort and returns the error for a command
// that cannot be queued.
func RefuseInTx(c *Client) resp.Value {
	c.tx.dirty = true
	return replyTxNoAllowed
}

// txCommands returns MULTI, EXEC, DISCARD, WATCH and UNWATCH.
func txCommands() []*Spec {
	fast := acl.CatTransaction | acl.CatFast
	return []*Spec{
		{Name: "multi", Arity: 1, Categories: fast, Tx: TxImmediate, Handler: multi},
		{Name: "exec", Arity: 1, Categories: acl.CatTransaction | acl.CatSlow, Tx: TxImmediate, Handler: execCmd},
		{Name: "discard", Arity: 1, Categories: fast, Tx: TxImmediate, Handler: discard},
		{Name: "watch", Arity: -2, Categories: fast, Tx: TxImmediate, FirstKey: 1, LastKey: -1, KeyStep: 1, Handler: watch},
		{Name: "unwatch", Arity: 1, Categories: fast, Handler: unwatch},
	}
}

// multi is MULTI.
func multi(_ *Env, c *Client, _ [][]byte) resp.Value {
	if c.tx.inMulti {
		return replyNested
	}
	c.tx.inMulti = true
	return replyOK
}

// discard is DISCARD. It also forgets the watched keys.
func discard(_ *Env, c *Client, _ [][]byte) resp.Value {
	if !c.tx.inMulti {
		return replyNoDiscard
	}
	c.resetTx()
	return replyOK
}

// watch is WATCH key [key ...]. EXEC runs only if none of them changed since.
func watch(env *Env, c *Client, args [][]byte) resp.Value {
	if c.tx.inMulti {
		return replyWatchMulti
	}
	for _, k := range args[1:] {
		if bad, ok := env.checkKey(k); !ok {
			return bad
		}
	}
	for _, k := range args[1:] {
		dup := false
		for _, w := range c.tx.watched {
			if string(w.key) == string(k) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		if env.MaxWatch > 0 && len(c.tx.watched) >= env.MaxWatch {
			return replyTooManyKeys
		}
		e := watchEntry{key: append([]byte(nil), k...), version: env.Versions.Load(k)}
		if rec, err := env.loadRecord(k); err == nil {
			e.live, e.deadline = true, rec.deadline
		} else if !isNotFound(err) {
			return env.storageError("watch", err)
		}
		c.tx.watched = append(c.tx.watched, e)
	}
	return replyOK
}

// unwatch is UNWATCH.
func unwatch(_ *Env, c *Client, _ [][]byte) resp.Value {
	c.tx.watched = nil
	return replyOK
}

// changed tells if a watched key changed since WATCH: a write counted, or a
// deadline that passed.
func (e *Env) changed(w watchEntry) bool {
	if e.Versions.Load(w.key) != w.version {
		return true
	}
	return w.live && w.deadline != 0 && w.deadline <= e.nowMillis()
}

// replyBytes returns the number of bytes of text in a reply.
func replyBytes(v resp.Value) int {
	n := len(v.Bytes)
	for _, e := range v.Elems {
		n += replyBytes(e)
	}
	return n
}

// runQueued runs one queued command. A panic of the handler is an error in its
// own slot and closes the connection, as outside a transaction.
func runQueued(env *Env, c *Client, q queuedCmd) (reply resp.Value) {
	defer func() {
		if r := recover(); r != nil {
			if env.Log != nil {
				env.Log.Error("command panic in a transaction", "client", c.ID, "command", q.spec.Name, "panic", fmt.Sprint(r))
			}
			c.Close = true
			reply = resp.Error("ERR internal error")
		}
	}()
	return q.spec.Handler(env, c, q.args)
}

// execCmd is EXEC. It locks every key that the queued commands and the WATCH name,
// checks the watched keys, and then runs the commands in order. A command that
// fails does not stop the others, and a change of a watched key makes the reply a
// null array and runs nothing. The writes of the commands are applied one after
// the other, so another client that writes the same keys waits, but a reader that
// takes no lock can see the transaction half applied.
func execCmd(env *Env, c *Client, _ [][]byte) resp.Value {
	if !c.tx.inMulti {
		return replyNotInMulti
	}
	queue, watched, dirty := c.tx.queue, c.tx.watched, c.tx.dirty
	c.resetTx()
	if dirty {
		return replyExecAbort
	}
	var keys [][]byte
	for _, w := range watched {
		keys = append(keys, w.key)
	}
	for _, q := range queue {
		keys = append(keys, KeysOf(q.spec, q.args)...)
	}
	stripes := env.Locks.LockKeys(len(keys), func(i int) []byte { return keys[i] })
	defer env.Locks.UnlockStripes(stripes)
	for _, w := range watched {
		if env.changed(w) {
			return resp.NullArray()
		}
	}
	c.tx.held = true
	defer func() { c.tx.held = false }()
	replies := make([]resp.Value, len(queue))
	total := 0
	for i, q := range queue {
		if env.Access != nil && !q.spec.NoAuth && !env.Access.Allowed(c.User, q.spec.Name, q.spec.Categories) {
			replies[i] = resp.Error("NOPERM User " + printable([]byte(c.User), 64) + " has no permissions to run the '" + q.spec.Name + "' command")
			continue
		}
		replies[i] = runQueued(env, c, q)
		total += replyBytes(replies[i])
	}
	if env.MaxReply > 0 && total > env.MaxReply {
		return replyReplyBig
	}
	return resp.Array(replies...)
}
