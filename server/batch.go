// SPDX-License-Identifier: MIT

package server

import (
	"time"

	"github.com/thalesdev/sediment/server/command"
)

const (
	// maxBatchSets is the largest number of SET commands that run as one write.
	maxBatchSets = 64

	// maxBatchBytes is the largest total size of the keys and values of the SET
	// commands that run as one write. The batch stops when it reaches it.
	maxBatchBytes = 1 << 20
)

// isPlainSet reports whether args is SET key value, with no option, in any case.
func isPlainSet(args [][]byte) bool {
	n := args[0]
	return len(args) == 3 && len(n) == 3 && n[0]|0x20 == 's' && n[1]|0x20 == 'e' && n[2]|0x20 == 't'
}

// canBatchSet reports whether this client may run SET, so that the checks of
// execute would not refuse it. If it may not, the command takes the normal path
// and gets its error.
func (c *conn) canBatchSet(args [][]byte) bool {
	if !c.client.Authed || c.s.set == nil || c.client.InMulti() {
		return false
	}
	return c.s.opts.Access == nil || c.s.env.Check(c.client.User, c.s.set, args) == ""
}

// batchSets runs first and the plain SET commands that follow it and that the
// connection already received, as one write, and writes the replies in the order
// of the commands. It stops at the first command that is not a plain SET, and
// returns it as carry, so that the loop runs it next. It never waits for the
// network, because it reads only commands that Pending says are whole. If reading
// a command fails, it still runs and answers the commands before it, and returns
// the error. written is false if the connection could not take a reply.
func (c *conn) batchSets(first [][]byte) (carry [][]byte, readErr error, written bool) {
	cmds := make([][][]byte, 1, 16)
	cmds[0] = first
	size := len(first[1]) + len(first[2])
	for len(cmds) < maxBatchSets && size < maxBatchBytes && c.r.Pending() {
		next, err := c.r.ReadCommand()
		if err != nil {
			readErr = err
			break
		}
		if !isPlainSet(next) || !c.canBatchSet(next) {
			carry = next
			break
		}
		cmds = append(cmds, next)
		size += len(next[1]) + len(next[2])
	}
	start := time.Now()
	c.client.Touch(&c.s.set.Name, start)
	replies := command.SetMany(c.s.env, cmds)
	c.s.observe(c.client, &c.s.set.Name, 2*len(cmds), start)
	c.s.totalCommands.Add(uint64(len(cmds)))
	c.s.setBatches.Add(1)
	c.s.setBatched.Add(uint64(len(cmds)))
	c.w.SetProtocol(c.client.Proto)
	for _, r := range replies {
		if err := c.w.WriteValue(r); err != nil {
			return nil, nil, false
		}
	}
	return carry, readErr, true
}
