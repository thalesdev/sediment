// SPDX-License-Identifier: MIT

package command

import (
	"strings"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

const connFast = acl.CatFast | acl.CatConnection

// connectionCommands returns PING, ECHO, QUIT, SELECT, AUTH, HELLO and CLIENT.
func connectionCommands() []*Spec {
	return []*Spec{
		{Name: "ping", Arity: -1, Categories: connFast, Handler: ping},
		{Name: "echo", Arity: 2, Categories: connFast, Handler: echo},
		{Name: "quit", Arity: -1, Categories: connFast, NoAuth: true, Tx: TxImmediate, Handler: quit},
		{Name: "select", Arity: 2, Categories: connFast, Handler: selectDB},
		{Name: "auth", Arity: -2, Categories: connFast, NoAuth: true, Tx: TxNoQueue, Handler: auth},
		{Name: "hello", Arity: -1, Categories: connFast, NoAuth: true, Tx: TxNoQueue, Handler: hello},
		{Name: "client", Arity: -2, Categories: acl.CatSlow | acl.CatConnection, Subs: []string{"id", "getname", "setname", "setinfo", "info", "list", "kill"}, Handler: clientCmd},
	}
}

// ping is PING [message].
func ping(_ *Env, _ *Client, args [][]byte) resp.Value {
	switch len(args) {
	case 1:
		return resp.Simple("PONG")
	case 2:
		return resp.Bulk(args[1])
	}
	return WrongArgs("ping")
}

// echo is ECHO message.
func echo(_ *Env, _ *Client, args [][]byte) resp.Value {
	return resp.Bulk(args[1])
}

// quit is QUIT.
func quit(_ *Env, c *Client, _ [][]byte) resp.Value {
	c.Close = true
	return replyOK
}

// selectDB is SELECT index. Only the database 0 exists.
func selectDB(_ *Env, _ *Client, args [][]byte) resp.Value {
	n, ok := parseInt(args[1])
	if !ok {
		return replyNotInt
	}
	if n != 0 {
		return resp.Error("ERR DB index is out of range")
	}
	return replyOK
}

// signIn checks the user and the password, and delays the reply after a
// failure. It returns an error reply, or the zero Value on success.
func signIn(env *Env, c *Client, user string, password []byte) (resp.Value, bool) {
	if env.Access == nil {
		return resp.Error("ERR AUTH called without any password configured. Are you sure your configuration is correct?"), false
	}
	if env.AuthLimit != nil && env.AuthLimit.Blocked(c.Addr) {
		if env.Log != nil {
			env.Log.Warn("sign in refused, too many failures from this address", "addr", c.Addr)
		}
		c.Close = true
		return resp.Error("ERR too many failed sign ins from this address, try again later"), false
	}
	if env.Access.Authenticate(user, password) {
		c.setUser(user)
		c.Authed = true
		c.AuthFailures = 0
		return resp.Value{}, true
	}
	c.AuthFailures++
	env.Access.Record("auth", "toplevel", "AUTH", printable([]byte(user), 64), strings.TrimSpace(c.Info().line(time.Now())))
	if env.AuthLimit != nil {
		env.AuthLimit.Failed(c.Addr)
	}
	if env.Log != nil {
		env.Log.Warn("sign in failed", "user", printable([]byte(user), 64), "addr", c.Addr, "failures", c.AuthFailures)
	}
	delay := env.AuthDelay
	for i := 1; i < c.AuthFailures && delay < env.AuthDelayMax; i++ {
		delay *= 2
	}
	time.Sleep(min(delay, env.AuthDelayMax))
	if env.MaxAuthFailures > 0 && c.AuthFailures >= env.MaxAuthFailures {
		c.Close = true
	}
	return resp.Error("WRONGPASS invalid username-password pair or user is disabled."), false
}

// auth is AUTH [user] password.
func auth(env *Env, c *Client, args [][]byte) resp.Value {
	user, password := "default", args[1]
	switch len(args) {
	case 2:
	case 3:
		user, password = string(args[1]), args[2]
	default:
		return replySyntax
	}
	if bad, ok := signIn(env, c, user, password); !ok {
		return bad
	}
	return replyOK
}

// validName reports whether s is a valid client name or library name: printable
// ASCII without spaces, of at most 128 bytes.
func validName(s []byte) bool {
	if len(s) > 128 {
		return false
	}
	for _, c := range s {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}

// hello is HELLO [protover [AUTH user password] [SETNAME name]].
func hello(env *Env, c *Client, args [][]byte) resp.Value {
	proto := c.Proto
	i := 1
	if len(args) > 1 {
		n, ok := parseInt(args[1])
		if !ok {
			return resp.Error("ERR Protocol version is not an integer or out of range")
		}
		switch n {
		case 2:
			proto = 2
		case 3:
			proto = 3
		default:
			return resp.Error("NOPROTO sorry, this protocol version is not supported")
		}
		i = 2
	}
	var user string
	var password, name []byte
	withAuth, withName := false, false
	for i < len(args) {
		more := len(args) - i - 1
		switch strings.ToUpper(string(args[i])) {
		case "AUTH":
			if more < 2 {
				return replySyntax
			}
			user, password, withAuth = string(args[i+1]), args[i+2], true
			i += 3
		case "SETNAME":
			if more < 1 {
				return replySyntax
			}
			name, withName = args[i+1], true
			i += 2
		default:
			return resp.Error("ERR Syntax error in HELLO option '" + printable(args[i], 32) + "'")
		}
	}
	if withName && !validName(name) {
		return resp.Error("ERR Client names cannot contain spaces, newlines or special characters.")
	}
	if withAuth {
		if bad, ok := signIn(env, c, user, password); !ok {
			return bad
		}
	} else if !c.Authed {
		return resp.Error("NOAUTH HELLO must be called with the client already authenticated, otherwise the HELLO <proto> AUTH <user> <pass> option can be used to authenticate the client and select the RESP protocol version at the same time")
	}
	c.setProto(proto)
	if withName {
		c.setName(string(name))
	}
	return resp.Map(
		resp.BulkString("server"), resp.BulkString("sediment"),
		resp.BulkString("version"), resp.BulkString(env.Version),
		resp.BulkString("proto"), resp.Integer(int64(c.Proto)),
		resp.BulkString("id"), resp.Integer(int64(c.ID)),
		resp.BulkString("mode"), resp.BulkString("standalone"),
		resp.BulkString("role"), resp.BulkString("master"),
		resp.BulkString("modules"), resp.Array(),
	)
}

// clientCmd is CLIENT ID, SETNAME, GETNAME, SETINFO, INFO, LIST and KILL.
func clientCmd(env *Env, c *Client, args [][]byte) resp.Value {
	switch strings.ToUpper(string(args[1])) {
	case "ID":
		if len(args) != 2 {
			return WrongArgs("client|id")
		}
		return resp.Integer(int64(c.ID))
	case "GETNAME":
		if len(args) != 2 {
			return WrongArgs("client|getname")
		}
		if c.Name == "" {
			return resp.Null()
		}
		return resp.BulkString(c.Name)
	case "SETNAME":
		if len(args) != 3 {
			return WrongArgs("client|setname")
		}
		if !validName(args[2]) {
			return resp.Error("ERR Client names cannot contain spaces, newlines or special characters.")
		}
		c.setName(string(args[2]))
		return replyOK
	case "SETINFO":
		return clientSetInfo(c, args)
	case "INFO":
		if len(args) != 2 {
			return WrongArgs("client|info")
		}
		return resp.Value{Kind: resp.KindVerbatim, Bytes: []byte(c.Info().line(time.Now())), Format: "txt"}
	case "LIST":
		return clientList(env, c, args)
	case "KILL":
		return clientKill(env, c, args)
	}
	return resp.Error("ERR unknown subcommand '" + printable(args[1], 32) + "' of CLIENT")
}

// clientSetInfo is CLIENT SETINFO LIB-NAME | LIB-VER value.
func clientSetInfo(c *Client, args [][]byte) resp.Value {
	if len(args) != 4 {
		return WrongArgs("client|setinfo")
	}
	if !validName(args[3]) {
		return resp.Error("ERR " + strings.ToLower(printable(args[2], 16)) + " cannot contain spaces, newlines or special characters.")
	}
	switch strings.ToUpper(string(args[2])) {
	case "LIB-NAME":
		c.setLib(string(args[3]), "")
	case "LIB-VER":
		c.setLib("", string(args[3]))
	default:
		return resp.Error("ERR Unrecognized option '" + printable(args[2], 32) + "'")
	}
	return replyOK
}
