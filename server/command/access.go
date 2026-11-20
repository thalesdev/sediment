// SPDX-License-Identifier: MIT

package command

import (
	"strings"
	"time"

	"github.com/thalesdev/sediment/resp"
)

// subOf returns the lower case name of the subcommand of a command that has
// subcommands, or "".
func subOf(spec *Spec, args [][]byte) string {
	if len(spec.Subs) == 0 || len(args) < 2 {
		return ""
	}
	return strings.ToLower(string(args[1]))
}

// Check returns "" if the user can run the command, or the reason that it cannot:
// "command" if the rules do not allow the command, and "key" if they allow it but
// not on a key that it uses, or if the command lists keys and the user cannot
// read all of them. It needs env.Access.
func (e *Env) Check(user string, spec *Spec, args [][]byte) string {
	sub := subOf(spec, args)
	if !e.Access.Allowed(user, spec.Name, sub, spec.Categories, KeysOf(spec, args)) {
		if !e.Access.Allowed(user, spec.Name, sub, spec.Categories, nil) {
			return "command"
		}
		return "key"
	}
	if spec.ListsKeys && !e.Access.AllKeysReadable(user) {
		return "key"
	}
	if spec.WritesAllKeys && !e.Access.AllKeysWritable(user) {
		return "key"
	}
	return ""
}

// Deny keeps the denial for ACL LOG and returns the error reply for the client.
// The reply never names a key.
func (e *Env) Deny(c *Client, spec *Spec, args [][]byte, reason, context string) resp.Value {
	e.Access.Record(reason, context, spec.Name, c.User, strings.TrimSpace(c.Info().line(time.Now())))
	if reason == "key" {
		return resp.Error("NOPERM No permissions to access a key that '" + spec.Name + "' needs")
	}
	name := spec.Name
	if sub := subOf(spec, args); sub != "" {
		name += "|" + printable([]byte(sub), 32)
	}
	return resp.Error("NOPERM User " + printable([]byte(c.User), 64) + " has no permissions to run the '" + name + "' command")
}
