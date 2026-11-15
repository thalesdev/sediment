// SPDX-License-Identifier: MIT

package command

import (
	"errors"
	"strconv"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
)

var (
	replyOK        = resp.Simple("OK")
	replySyntax    = resp.Error("ERR syntax error")
	replyNotInt    = resp.Error("ERR value is not an integer or out of range")
	replyKeyLarge  = resp.Error("ERR key is too large")
	replyReplyBig  = resp.Error("ERR reply is too large")
	replyStorage   = resp.Error("ERR storage error")
	replyNoAuthYet = resp.Error("NOAUTH Authentication required.")
	replyWrongType = resp.Error("WRONGTYPE Operation against a key holding the wrong kind of value")
)

// isNotFound reports whether err says that a key does not exist.
func isNotFound(err error) bool {
	return errors.Is(err, shale.ErrNotFound)
}

// WrongArgs returns the error for a command with the wrong number of arguments.
func WrongArgs(name string) resp.Value {
	return resp.Error("ERR wrong number of arguments for '" + name + "' command")
}

// storageError logs err and returns a reply that does not tell the client what
// went wrong inside. The engine error can hold file names.
func (e *Env) storageError(command string, err error) resp.Value {
	if errors.Is(err, errWrongType) {
		return replyWrongType
	}
	if e.Log != nil {
		e.Log.Error("storage error", "command", command, "err", err)
	}
	return replyStorage
}

// checkKey returns an error reply if the key is too large, or the zero Value.
func (e *Env) checkKey(key []byte) (resp.Value, bool) {
	if e.MaxKey > 0 && len(key) > e.MaxKey {
		return replyKeyLarge, false
	}
	return resp.Value{}, true
}

// printable returns s with each byte that is not printable ASCII replaced by a
// question mark, cut to max bytes, so that it is safe to put in a reply and in a
// log.
func printable(s []byte, max int) string {
	if len(s) > max {
		s = s[:max]
	}
	out := make([]byte, len(s))
	for i, c := range s {
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		out[i] = c
	}
	return string(out)
}

// parseInt parses a decimal integer.
func parseInt(b []byte) (int64, bool) {
	n, err := strconv.ParseInt(string(b), 10, 64)
	return n, err == nil
}
