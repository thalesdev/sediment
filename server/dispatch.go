// SPDX-License-Identifier: MIT

package server

import (
	"runtime/debug"
	"strings"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/command"
)

// execute checks and runs one command, and returns the reply. A panic of the
// command becomes an error reply, and the connection closes after it.
func (c *conn) execute(args [][]byte) (reply resp.Value) {
	defer func() {
		if r := recover(); r != nil {
			c.s.opts.Log.Error("command panic", "client", c.client.ID, "panic", r, "stack", string(debug.Stack()))
			c.client.Close = true
			reply = resp.Error("ERR internal error")
		}
	}()
	s := c.s
	spec := s.reg.Lookup(args[0])
	if spec == nil {
		c.client.MarkTxDirty()
		return resp.Error("ERR unknown command '" + printable(args[0], 64) + "'" + argsPreview(args[1:]))
	}
	if (spec.Arity > 0 && len(args) != spec.Arity) || (spec.Arity < 0 && len(args) < -spec.Arity) {
		c.client.MarkTxDirty()
		return command.WrongArgs(spec.Name)
	}
	if !c.client.Authed && !spec.NoAuth {
		s.deniedCmds.Add(1)
		s.opts.Log.Debug("command refused before sign in", "addr", c.client.Addr, "command", spec.Name)
		return resp.Error("NOAUTH Authentication required.")
	}
	if s.opts.Access != nil && !spec.NoAuth {
		if reason := s.env.Check(c.client.User, spec, args); reason != "" {
			s.deniedCmds.Add(1)
			c.client.MarkTxDirty()
			s.opts.Log.Warn("command denied", "user", printable([]byte(c.client.User), 64), "addr", c.client.Addr, "command", spec.Name, "reason", reason)
			return s.env.Deny(c.client, spec, args, reason, "toplevel")
		}
	}
	if c.client.InMulti() {
		switch spec.Tx {
		case command.TxQueue:
			return command.Enqueue(s.env, c.client, spec, args)
		case command.TxNoQueue:
			return command.RefuseInTx(c.client)
		}
	}
	s.totalCommands.Add(1)
	start := time.Now()
	c.client.Touch(&spec.Name, start)
	reply = spec.Handler(s.env, c.client, args)
	s.observe(c.client, &spec.Name, len(args)-1, start)
	return reply
}

// printable returns b cut to max bytes, with each byte that is not printable
// ASCII replaced by a question mark.
func printable(b []byte, max int) string {
	if len(b) > max {
		b = b[:max]
	}
	var sb strings.Builder
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		sb.WriteByte(c)
	}
	return sb.String()
}

// argsPreview returns the start of the arguments of an unknown command, as
// Redis does, cut so that a client cannot make the server echo a large input.
func argsPreview(args [][]byte) string {
	if len(args) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(", with args beginning with:")
	for i, a := range args {
		if i == 3 {
			break
		}
		sb.WriteString(" '")
		sb.WriteString(printable(a, 32))
		sb.WriteString("'")
	}
	return sb.String()
}
