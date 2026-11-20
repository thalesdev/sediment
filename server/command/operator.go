// SPDX-License-Identifier: MIT

package command

import (
	"strconv"
	"strings"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

// ClientInfo is a copy of the state of one client at one time.
type ClientInfo struct {
	// ID is the number of the client.
	ID uint64

	// Addr and LAddr are the remote and the local address.
	Addr, LAddr string

	// Name, LibName and LibVer are what the client set.
	Name, LibName, LibVer string

	// User is the name of the user that signed in.
	User string

	// Cmd is the name of the last command that the client ran.
	Cmd string

	// Proto is the version of the protocol, 2 or 3.
	Proto int

	// Created is the time of the connection, and LastActive the time of the last
	// command.
	Created, LastActive time.Time
}

// SlowEntry is one command of the slow log. It holds the name of the command and
// the number of its arguments, and never an argument, because an argument can be
// a key, a value or a password.
type SlowEntry struct {
	// ID is the number of the entry, which grows by one for each entry.
	ID uint64

	// At is the time that the command started.
	At time.Time

	// Took is the time that the command took.
	Took time.Duration

	// Command is the name of the command.
	Command string

	// Args is the number of arguments after the name.
	Args int

	// Addr and Name are the address and the name of the client.
	Addr, Name string
}

// operatorCommands returns SLOWLOG.
func operatorCommands() []*Spec {
	return []*Spec{
		{Name: "slowlog", Arity: -2, Categories: acl.CatSlow | acl.CatAdmin | acl.CatDangerous, Subs: []string{"get", "len", "reset"}, Handler: slowlog},
	}
}

// setUser, setProto, setName and setLib change the state that CLIENT LIST shows to
// the other clients. They hold the lock of the client, and the goroutine of the
// client reads its own fields with no lock, because it is the only one that writes.
func (c *Client) setUser(user string) {
	c.mu.Lock()
	c.User = user
	c.mu.Unlock()
}

// setProto sets the protocol of the client.
func (c *Client) setProto(proto int) {
	c.mu.Lock()
	c.Proto = proto
	c.mu.Unlock()
}

// setName sets the name of the client.
func (c *Client) setName(name string) {
	c.mu.Lock()
	c.Name = name
	c.mu.Unlock()
}

// setLib sets the library name, or the library version, if it is not empty.
func (c *Client) setLib(name, ver string) {
	c.mu.Lock()
	if name != "" {
		c.LibName = name
	}
	if ver != "" {
		c.LibVer = ver
	}
	c.mu.Unlock()
}

// SignInAs signs the client in as the user, with no password. The server calls it for
// a client whose certificate names a user.
func (c *Client) SignInAs(user string) {
	c.setUser(user)
	c.Authed = true
}

// Touch records that the client runs the command cmd at now. The server calls it for
// each command, from the goroutine of the client.
func (c *Client) Touch(cmd *string, now time.Time) {
	c.cmd.Store(cmd)
	c.active.Store(now.UnixNano())
}

// Info returns a copy of the state of c. Any goroutine can call it.
func (c *Client) Info() ClientInfo {
	c.mu.Lock()
	info := ClientInfo{
		ID: c.ID, Addr: c.Addr, LAddr: c.LAddr, Name: c.Name, LibName: c.LibName,
		LibVer: c.LibVer, User: c.User, Proto: c.Proto, Created: c.Created,
	}
	c.mu.Unlock()
	info.LastActive = info.Created
	if n := c.active.Load(); n != 0 {
		info.LastActive = time.Unix(0, n)
	}
	if cmd := c.cmd.Load(); cmd != nil {
		info.Cmd = *cmd
	}
	return info
}

// line returns the CLIENT LIST line of the client, as of now.
func (i ClientInfo) line(now time.Time) string {
	var b strings.Builder
	field := func(k, v string) {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte(' ')
	}
	field("id", strconv.FormatUint(i.ID, 10))
	field("addr", i.Addr)
	field("laddr", i.LAddr)
	field("name", i.Name)
	field("age", strconv.FormatInt(max(0, int64(now.Sub(i.Created)/time.Second)), 10))
	field("idle", strconv.FormatInt(max(0, int64(now.Sub(i.LastActive)/time.Second)), 10))
	field("flags", "N")
	field("cmd", i.Cmd)
	field("user", i.User)
	field("resp", strconv.Itoa(i.Proto))
	field("lib-name", i.LibName)
	b.WriteString("lib-ver=")
	b.WriteString(i.LibVer)
	b.WriteByte('\n')
	return b.String()
}

// needAdmin returns an error reply if the user of c has no right to run the
// commands of an operator, or the zero Value. The check is by the categories admin
// and dangerous, so that a user who can run CLIENT but not these cannot list or
// kill the other clients.
func needAdmin(env *Env, c *Client, sub string) (resp.Value, bool) {
	if env.Access != nil && !env.Access.Allowed(c.User, "client", sub, acl.CatAdmin|acl.CatDangerous, nil) {
		return resp.Error("NOPERM User " + printable([]byte(c.User), 64) + " has no permissions to run the 'client|" + sub + "' command"), false
	}
	return resp.Value{}, true
}

// clientList is CLIENT LIST [TYPE normal] [ID id ...].
func clientList(env *Env, c *Client, args [][]byte) resp.Value {
	if bad, ok := needAdmin(env, c, "list"); !ok {
		return bad
	}
	if env.Operator == nil {
		return resp.Error("ERR the server cannot list its clients")
	}
	var ids map[uint64]bool
	for i := 2; i < len(args); {
		switch strings.ToUpper(string(args[i])) {
		case "TYPE":
			if i+1 >= len(args) {
				return replySyntax
			}
			switch strings.ToLower(string(args[i+1])) {
			case "normal":
			case "master", "replica", "slave", "pubsub":
				return resp.Value{Kind: resp.KindVerbatim, Bytes: nil, Format: "txt"}
			default:
				return resp.Error("ERR Unknown client type '" + printable(args[i+1], 32) + "'")
			}
			i += 2
		case "ID":
			if i+1 >= len(args) {
				return replySyntax
			}
			ids = map[uint64]bool{}
			for i++; i < len(args); i++ {
				n, ok := parseInt(args[i])
				if !ok || n <= 0 {
					return resp.Error("ERR Invalid client ID")
				}
				ids[uint64(n)] = true
			}
		default:
			return replySyntax
		}
	}
	now := time.Now()
	var b strings.Builder
	for _, info := range env.Operator.Clients() {
		if ids != nil && !ids[info.ID] {
			continue
		}
		b.WriteString(info.line(now))
	}
	return resp.Value{Kind: resp.KindVerbatim, Bytes: []byte(b.String()), Format: "txt"}
}

// killFilter is what CLIENT KILL matches.
type killFilter struct {
	id          uint64
	addr, laddr string
	user        string
	hasID       bool
	hasAddr     bool
	hasLAddr    bool
	hasUser     bool
	skipMe      bool
}

// matches reports whether the client matches every part of the filter.
func (f killFilter) matches(i ClientInfo, self uint64) bool {
	switch {
	case f.skipMe && i.ID == self:
		return false
	case f.hasID && i.ID != f.id:
		return false
	case f.hasAddr && i.Addr != f.addr:
		return false
	case f.hasLAddr && i.LAddr != f.laddr:
		return false
	case f.hasUser && i.User != f.user:
		return false
	}
	return true
}

// clientKill is CLIENT KILL addr, and CLIENT KILL with ID, ADDR, LADDR, USER and
// SKIPME. The old form answers OK or an error, and the new one the number of
// connections that it closed.
func clientKill(env *Env, c *Client, args [][]byte) resp.Value {
	if bad, ok := needAdmin(env, c, "kill"); !ok {
		return bad
	}
	if env.Operator == nil {
		return resp.Error("ERR the server cannot list its clients")
	}
	if len(args) == 3 {
		f := killFilter{addr: string(args[2]), hasAddr: true}
		if closeMatching(env, c, f) == 0 {
			return resp.Error("ERR No such client")
		}
		return replyOK
	}
	if len(args) < 4 || len(args)%2 != 0 {
		return replySyntax
	}
	f := killFilter{skipMe: true}
	for i := 2; i < len(args); i += 2 {
		val := args[i+1]
		switch strings.ToUpper(string(args[i])) {
		case "ID":
			n, ok := parseInt(val)
			if !ok || n <= 0 {
				return resp.Error("ERR client-id should be greater than 0")
			}
			f.id, f.hasID = uint64(n), true
		case "ADDR":
			f.addr, f.hasAddr = string(val), true
		case "LADDR":
			f.laddr, f.hasLAddr = string(val), true
		case "USER":
			f.user, f.hasUser = string(val), true
		case "SKIPME":
			switch strings.ToLower(string(val)) {
			case "yes":
				f.skipMe = true
			case "no":
				f.skipMe = false
			default:
				return replySyntax
			}
		case "TYPE":
			if !strings.EqualFold(string(val), "normal") {
				return resp.Integer(0)
			}
		default:
			return replySyntax
		}
	}
	return resp.Integer(int64(closeMatching(env, c, f)))
}

// closeMatching closes the connection of each client that f matches, and returns
// how many it closed. The connection of c itself closes after its reply.
func closeMatching(env *Env, c *Client, f killFilter) int {
	n := 0
	for _, info := range env.Operator.Clients() {
		if !f.matches(info, c.ID) {
			continue
		}
		if info.ID == c.ID {
			c.Close = true
			n++
			continue
		}
		if env.Operator.Kill(info.ID) {
			n++
		}
	}
	return n
}

// slowlog is SLOWLOG GET [count], LEN, RESET and HELP.
func slowlog(env *Env, _ *Client, args [][]byte) resp.Value {
	if env.Operator == nil {
		return resp.Error("ERR the server has no slow log")
	}
	switch strings.ToUpper(string(args[1])) {
	case "GET":
		n := 10
		switch len(args) {
		case 2:
		case 3:
			v, ok := parseInt(args[2])
			if !ok || v < -1 {
				return resp.Error("ERR count should be greater than or equal to -1")
			}
			n = toInt(v)
		default:
			return WrongArgs("slowlog|get")
		}
		entries := env.Operator.SlowLog(n)
		out := make([]resp.Value, len(entries))
		for i, e := range entries {
			args := []resp.Value{resp.BulkString(e.Command)}
			if e.Args > 0 {
				args = append(args, resp.BulkString("... ("+strconv.Itoa(e.Args)+" arguments)"))
			}
			out[i] = resp.Array(
				resp.Integer(int64(e.ID)),
				resp.Integer(e.At.Unix()),
				resp.Integer(e.Took.Microseconds()),
				resp.Array(args...),
				resp.BulkString(e.Addr),
				resp.BulkString(e.Name),
			)
		}
		return resp.Array(out...)
	case "LEN":
		if len(args) != 2 {
			return WrongArgs("slowlog|len")
		}
		return resp.Integer(int64(env.Operator.SlowLogLen()))
	case "RESET":
		if len(args) != 2 {
			return WrongArgs("slowlog|reset")
		}
		env.Operator.SlowLogReset()
		return replyOK
	}
	return resp.Error("ERR unknown subcommand '" + printable(args[1], 32) + "' of SLOWLOG")
}
