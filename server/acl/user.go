// SPDX-License-Identifier: MIT

package acl

import (
	"errors"
	"fmt"
	"strings"
)

// commandRule is one +@category, -@category, +command or -command of a user.
type commandRule struct {
	allow    bool
	all      bool
	category Category
	command  string
	text     string
}

// matches reports whether the rule covers the command.
func (r commandRule) matches(command string, cats Category) bool {
	switch {
	case r.all:
		return true
	case r.command != "":
		return r.command == command
	}
	return cats&r.category != 0
}

// User is a user of the server.
type User struct {
	name      string
	enabled   bool
	noPass    bool
	passwords []password
	rules     []commandRule
}

// Name returns the name of the user.
func (u *User) Name() string {
	return u.name
}

// Enabled reports whether the user can sign in.
func (u *User) Enabled() bool {
	return u.enabled
}

// allowed reports whether the user can run the command. The rules apply in
// order, and the last rule that covers the command decides. With no rule, the
// user can run nothing.
func (u *User) allowed(command string, cats Category) bool {
	if !u.enabled {
		return false
	}
	ok := false
	for _, r := range u.rules {
		if r.matches(command, cats) {
			ok = r.allow
		}
	}
	return ok
}

// describe returns the user as a line of the ACL file.
func (u *User) describe() string {
	var b strings.Builder
	b.WriteString("user ")
	b.WriteString(u.name)
	if u.enabled {
		b.WriteString(" on")
	} else {
		b.WriteString(" off")
	}
	if u.noPass {
		b.WriteString(" nopass")
	}
	for _, p := range u.passwords {
		b.WriteString(" ")
		b.WriteString(p.String())
	}
	b.WriteString(" ~*")
	for _, r := range u.rules {
		b.WriteString(" ")
		b.WriteString(r.text)
	}
	return b.String()
}

// ParseOptions changes how rules are read.
type ParseOptions struct {
	// KnownCommand reports whether the server has a command. If it is nil, any
	// command name is accepted.
	KnownCommand func(name string) bool
}

// applyRule changes u with one rule of the ACL file. It returns whether the rule
// had a plain password, so that the caller can warn about it.
func (u *User) applyRule(tok string, o ParseOptions) (plain bool, err error) {
	switch {
	case tok == "on":
		u.enabled = true
	case tok == "off":
		u.enabled = false
	case tok == "nopass":
		u.noPass = true
		u.passwords = nil
	case tok == "resetpass":
		u.noPass = false
		u.passwords = nil
	case tok == "reset":
		*u = User{name: u.name}
	case strings.HasPrefix(tok, ">"):
		hash, err := HashPassword([]byte(tok[1:]))
		if err != nil {
			return false, err
		}
		p, err := parsePassword(hash)
		if err != nil {
			return false, err
		}
		u.noPass = false
		u.passwords = append(u.passwords, p)
		return true, nil
	case strings.HasPrefix(tok, "#") || strings.HasPrefix(tok, "$"):
		p, err := parsePassword(tok)
		if err != nil {
			return false, err
		}
		u.noPass = false
		u.passwords = append(u.passwords, p)
	case tok == "allkeys" || tok == "~*":
	case strings.HasPrefix(tok, "~") || strings.HasPrefix(tok, "%") || tok == "resetkeys":
		return false, errors.New("key patterns other than ~* are not supported yet")
	case strings.HasPrefix(tok, "&") || tok == "allchannels" || tok == "resetchannels":
	case tok == "allcommands" || tok == "+@all":
		u.rules = append(u.rules, commandRule{allow: true, all: true, text: "+@all"})
	case tok == "nocommands" || tok == "-@all":
		u.rules = append(u.rules, commandRule{allow: false, all: true, text: "-@all"})
	case strings.HasPrefix(tok, "+") || strings.HasPrefix(tok, "-"):
		return false, u.commandRule(tok, o)
	default:
		return false, fmt.Errorf("unknown rule %q", printable(tok))
	}
	return false, nil
}

// commandRule adds the rule of a token that starts with + or -.
func (u *User) commandRule(tok string, o ParseOptions) error {
	allow := tok[0] == '+'
	name := tok[1:]
	if strings.HasPrefix(name, "@") {
		cat, ok := ParseCategory(name[1:])
		if !ok {
			return fmt.Errorf("unknown category %q", printable(name))
		}
		u.rules = append(u.rules, commandRule{allow: allow, category: cat, text: string(tok[0]) + "@" + strings.ToLower(name[1:])})
		return nil
	}
	if strings.Contains(name, "|") {
		return errors.New("rules for subcommands are not supported yet")
	}
	name = strings.ToLower(name)
	if name == "" || (o.KnownCommand != nil && !o.KnownCommand(name)) {
		return fmt.Errorf("unknown command %q", printable(name))
	}
	u.rules = append(u.rules, commandRule{allow: allow, command: name, text: string(tok[0]) + name})
	return nil
}

// printable returns s cut to 64 bytes, with each byte that is not printable
// ASCII replaced by a question mark, so that it is safe in an error message.
func printable(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	b := []byte(s)
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		}
	}
	return string(b)
}
