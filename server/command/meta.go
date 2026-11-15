// SPDX-License-Identifier: MIT

package command

import (
	"strconv"
	"strings"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

// itoa returns the decimal text of n.
func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// metaCommands returns COMMAND, INFO and CONFIG.
func metaCommands() []*Spec {
	return []*Spec{
		{Name: "command", Arity: -1, Categories: acl.CatSlow | acl.CatConnection, Handler: commandCmd},
		{Name: "info", Arity: -1, Categories: acl.CatSlow | acl.CatDangerous, Handler: info},
		{Name: "config", Arity: -2, Categories: acl.CatSlow | acl.CatAdmin | acl.CatDangerous, Handler: config},
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
