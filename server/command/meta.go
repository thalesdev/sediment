// SPDX-License-Identifier: MIT

package command

import (
	"strconv"
	"strings"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

// itoa returns the decimal text of n.
func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// metaCommands returns COMMAND, INFO, ACL and CONFIG.
func metaCommands() []*Spec {
	return []*Spec{
		{Name: "command", Arity: -1, Categories: acl.CatSlow | acl.CatConnection, Subs: []string{"info", "count", "list"}, Handler: commandCmd},
		{Name: "info", Arity: -1, Categories: acl.CatSlow | acl.CatDangerous, Handler: info},
		{Name: "acl", Arity: -2, Categories: acl.CatSlow | acl.CatAdmin | acl.CatDangerous, Subs: []string{"whoami", "cat", "users", "list", "load", "setuser", "deluser", "getuser", "save", "log"}, Handler: aclCmd},
		{Name: "config", Arity: -2, Categories: acl.CatSlow | acl.CatAdmin | acl.CatDangerous, Subs: []string{"get"}, Handler: config},
	}
}

// info is INFO [section].
func info(env *Env, _ *Client, args [][]byte) resp.Value {
	section := ""
	if len(args) > 1 {
		section = strings.ToLower(string(args[1]))
	}
	if env.Meta == nil {
		return resp.Value{Kind: resp.KindVerbatim, Format: "txt"}
	}
	return resp.Value{Kind: resp.KindVerbatim, Format: "txt", Bytes: []byte(env.Meta.Info(section))}
}

// config is CONFIG GET pattern [pattern ...]. The server cannot change its
// settings while it runs.
func config(env *Env, _ *Client, args [][]byte) resp.Value {
	if !strings.EqualFold(string(args[1]), "GET") {
		return resp.Error("ERR unknown subcommand or not supported '" + printable(args[1], 32) + "' of CONFIG")
	}
	if len(args) < 3 {
		return WrongArgs("config|get")
	}
	var out []resp.Value
	seen := map[string]bool{}
	if env.Meta != nil {
		for _, pattern := range args[2:] {
			for _, kv := range env.Meta.Config(strings.ToLower(string(pattern))) {
				if seen[kv[0]] {
					continue
				}
				seen[kv[0]] = true
				out = append(out, resp.BulkString(kv[0]), resp.BulkString(kv[1]))
			}
		}
	}
	return resp.Map(out...)
}

// commandCmd is COMMAND, COMMAND COUNT, COMMAND LIST, COMMAND INFO and COMMAND
// DOCS.
func commandCmd(env *Env, _ *Client, args [][]byte) resp.Value {
	specs := env.Registry.Specs()
	if len(args) == 1 {
		out := make([]resp.Value, len(specs))
		for i, s := range specs {
			out[i] = specInfo(s)
		}
		return resp.Array(out...)
	}
	switch strings.ToUpper(string(args[1])) {
	case "COUNT":
		return resp.Integer(int64(len(specs)))
	case "LIST":
		out := make([]resp.Value, len(specs))
		for i, s := range specs {
			out[i] = resp.BulkString(s.Name)
		}
		return resp.Array(out...)
	case "DOCS":
		return resp.Map()
	case "INFO":
		out := make([]resp.Value, 0, len(args)-2)
		for _, name := range args[2:] {
			if s := env.Registry.Lookup(name); s != nil {
				out = append(out, specInfo(s))
			} else {
				out = append(out, resp.Null())
			}
		}
		return resp.Array(out...)
	}
	return resp.Error("ERR unknown subcommand '" + printable(args[1], 32) + "' of COMMAND")
}

// specInfo returns the COMMAND entry of s.
func specInfo(s *Spec) resp.Value {
	var flags []resp.Value
	if s.Categories&acl.CatWrite != 0 {
		flags = append(flags, resp.Simple("write"))
	} else if s.Categories&acl.CatRead != 0 {
		flags = append(flags, resp.Simple("readonly"))
	}
	if s.Categories&acl.CatFast != 0 {
		flags = append(flags, resp.Simple("fast"))
	}
	if s.Categories&acl.CatAdmin != 0 {
		flags = append(flags, resp.Simple("admin"))
	}
	if s.NoAuth {
		flags = append(flags, resp.Simple("no_auth"))
	}
	cats := make([]resp.Value, 0, 4)
	for _, n := range s.Categories.Names() {
		cats = append(cats, resp.Simple(n))
	}
	return resp.Array(
		resp.BulkString(s.Name), resp.Integer(int64(s.Arity)), resp.Array(flags...),
		resp.Integer(int64(s.FirstKey)), resp.Integer(int64(s.LastKey)), resp.Integer(int64(s.KeyStep)),
		resp.Array(cats...),
	)
}

// aclCmd is ACL WHOAMI, USERS, LIST, LOAD and CAT.
func aclCmd(env *Env, c *Client, args [][]byte) resp.Value {
	sub := strings.ToUpper(string(args[1]))
	if sub == "WHOAMI" {
		return resp.BulkString(c.User)
	}
	if sub == "CAT" {
		if len(args) != 2 {
			return WrongArgs("acl|cat")
		}
		var out []resp.Value
		for _, n := range acl.CatAll.Names() {
			out = append(out, resp.BulkString(strings.TrimPrefix(n, "@")))
		}
		return resp.Array(out...)
	}
	if env.Access == nil {
		return resp.Error("ERR this server has no ACL file")
	}
	switch sub {
	case "USERS":
		var out []resp.Value
		for _, n := range env.Access.Users() {
			out = append(out, resp.BulkString(n))
		}
		return resp.Array(out...)
	case "LIST":
		var out []resp.Value
		for _, l := range env.Access.List() {
			out = append(out, resp.BulkString(l))
		}
		return resp.Array(out...)
	case "LOAD":
		warnings, err := env.Access.Reload()
		if err != nil {
			if env.Log != nil {
				env.Log.Error("acl load failed", "err", err)
			}
			return resp.Error("ERR the ACL file was not loaded, the users did not change: " + printable([]byte(err.Error()), 200))
		}
		if env.Log != nil {
			for _, w := range warnings {
				env.Log.Warn("acl", "warning", w)
			}
		}
		closeStaleUsers(env, c)
		return replyOK
	case "SETUSER":
		if len(args) < 3 {
			return WrongArgs("acl|setuser")
		}
		rules := make([]string, len(args)-3)
		for i, a := range args[3:] {
			rules[i] = string(a)
		}
		warnings, err := env.Access.SetUser(string(args[2]), rules)
		if err != nil {
			return resp.Error("ERR Error in ACL SETUSER modifier: " + printable([]byte(err.Error()), 200))
		}
		logWarnings(env, warnings)
		closeStaleUsers(env, c)
		return replyOK
	case "DELUSER":
		if len(args) < 3 {
			return WrongArgs("acl|deluser")
		}
		names := make([]string, len(args)-2)
		for i, a := range args[2:] {
			names[i] = string(a)
		}
		n, err := env.Access.DelUser(names)
		if err != nil {
			return resp.Error("ERR " + printable([]byte(err.Error()), 200))
		}
		closeStaleUsers(env, c)
		return resp.Integer(int64(n))
	case "GETUSER":
		if len(args) != 3 {
			return WrongArgs("acl|getuser")
		}
		info, ok := env.Access.Info(string(args[2]))
		if !ok {
			return resp.Null()
		}
		return userInfoReply(info)
	case "SAVE":
		if len(args) != 2 {
			return WrongArgs("acl|save")
		}
		if err := env.Access.Save(); err != nil {
			if env.Log != nil {
				env.Log.Error("acl save failed", "err", err)
			}
			return resp.Error("ERR the ACL file was not saved: " + printable([]byte(err.Error()), 200))
		}
		return replyOK
	case "LOG":
		return aclLog(env, args)
	}
	return resp.Error("ERR unknown subcommand '" + printable(args[1], 32) + "' of ACL")
}

// logWarnings logs the warnings of a change of the users.
func logWarnings(env *Env, warnings []string) {
	if env.Log == nil {
		return
	}
	for _, w := range warnings {
		env.Log.Warn("acl", "warning", w)
	}
}

// closeStaleUsers closes the connections of the users that no longer exist or are
// off, after a change of the users. If the user of the caller is one of them, the
// connection of the caller closes after its reply.
func closeStaleUsers(env *Env, c *Client) {
	if env.Operator != nil {
		env.Operator.CloseWhere(env.Access.Active, c.ID)
	}
	if !env.Access.Active(c.User) {
		c.Close = true
	}
}

// userInfoReply returns the reply of ACL GETUSER.
func userInfoReply(info acl.UserInfo) resp.Value {
	list := func(items []string) resp.Value {
		out := make([]resp.Value, len(items))
		for i, s := range items {
			out[i] = resp.BulkString(s)
		}
		return resp.Array(out...)
	}
	selectors := make([]resp.Value, len(info.Selectors))
	for i, s := range info.Selectors {
		selectors[i] = resp.Map(
			resp.BulkString("commands"), resp.BulkString(s.Commands),
			resp.BulkString("keys"), resp.BulkString(s.Keys),
			resp.BulkString("channels"), resp.BulkString(""),
		)
	}
	return resp.Map(
		resp.BulkString("flags"), list(info.Flags),
		resp.BulkString("passwords"), list(info.Passwords),
		resp.BulkString("commands"), resp.BulkString(info.Commands),
		resp.BulkString("keys"), resp.BulkString(info.Keys),
		resp.BulkString("channels"), resp.BulkString(""),
		resp.BulkString("selectors"), resp.Array(selectors...),
	)
}

// aclLog is ACL LOG [count | RESET].
func aclLog(env *Env, args [][]byte) resp.Value {
	n := 10
	switch len(args) {
	case 2:
	case 3:
		if strings.EqualFold(string(args[2]), "RESET") {
			env.Access.LogReset()
			return replyOK
		}
		v, ok := parseInt(args[2])
		if !ok || v < 0 {
			return resp.Error("ERR value is out of range, must be positive")
		}
		n = toInt(v)
	default:
		return WrongArgs("acl|log")
	}
	now := time.Now()
	entries := env.Access.LogEntries(n)
	out := make([]resp.Value, len(entries))
	for i, e := range entries {
		out[i] = resp.Map(
			resp.BulkString("count"), resp.Integer(1),
			resp.BulkString("reason"), resp.BulkString(e.Reason),
			resp.BulkString("context"), resp.BulkString(e.Context),
			resp.BulkString("object"), resp.BulkString(e.Object),
			resp.BulkString("username"), resp.BulkString(e.User),
			resp.BulkString("age-seconds"), resp.BulkString(strconv.FormatFloat(now.Sub(e.At).Seconds(), 'f', 3, 64)),
			resp.BulkString("client-info"), resp.BulkString(e.Client),
			resp.BulkString("entry-id"), resp.Integer(int64(e.ID)),
			resp.BulkString("timestamp-created"), resp.Integer(e.At.UnixMilli()),
			resp.BulkString("timestamp-last-updated"), resp.Integer(e.At.UnixMilli()),
		)
	}
	return resp.Array(out...)
}
