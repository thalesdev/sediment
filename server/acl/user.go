// SPDX-License-Identifier: MIT

package acl

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/thalesdev/sediment/internal/glob"
)

// commandRule is one +@category, -@category, +command, -command, +command|sub or
// -command|sub of a user.
type commandRule struct {
	allow    bool
	all      bool
	category Category
	command  string
	sub      string
	text     string
}

// matches reports whether the rule covers the command and its subcommand.
func (r commandRule) matches(command, sub string, cats Category) bool {
	switch {
	case r.all:
		return true
	case r.command != "":
		return r.command == command && (r.sub == "" || r.sub == sub)
	}
	return cats&r.category != 0
}

// keyRule is one ~pattern, %R~pattern or %W~pattern of a user.
type keyRule struct {
	read, write bool
	pattern     string
}

// text returns the rule as it is written in the ACL file.
func (k keyRule) text() string {
	switch {
	case k.read && k.write:
		return "~" + k.pattern
	case k.read:
		return "%R~" + k.pattern
	}
	return "%W~" + k.pattern
}

// permSet is a set of command rules and key patterns. A user has one for the
// rules of the line, and one for each selector, and a command is allowed if any of
// them allows it.
type permSet struct {
	rules []commandRule
	keys  []keyRule
}

// commandAllowed reports whether the command rules allow the command. The rules
// apply in order, and the last rule that covers the command decides. With no rule,
// the set allows no command.
func (p *permSet) commandAllowed(command, sub string, cats Category) bool {
	ok := false
	for _, r := range p.rules {
		if r.matches(command, sub, cats) {
			ok = r.allow
		}
	}
	return ok
}

// keyAllowed reports whether a key rule lets the set read, or write, the key.
func (p *permSet) keyAllowed(key []byte, write bool) bool {
	for _, k := range p.keys {
		if (write && !k.write) || (!write && !k.read) {
			continue
		}
		if k.pattern == "*" || glob.Match([]byte(k.pattern), key) {
			return true
		}
	}
	return false
}

// allows reports whether the set allows the command on the keys.
func (p *permSet) allows(command, sub string, cats Category, keys [][]byte) bool {
	if !p.commandAllowed(command, sub, cats) {
		return false
	}
	write := cats&CatWrite != 0
	for _, key := range keys {
		if !p.keyAllowed(key, write) {
			return false
		}
	}
	return true
}

// allKeys reports whether the set can read every key, or write every key if write
// is true.
func (p *permSet) allKeys(write bool) bool {
	for _, k := range p.keys {
		if k.pattern == "*" && ((write && k.write) || (!write && k.read)) {
			return true
		}
	}
	return false
}

// text returns the rules of the set as words of the ACL file.
func (p *permSet) text() []string {
	var out []string
	for _, k := range p.keys {
		out = append(out, k.text())
	}
	for _, r := range p.rules {
		out = append(out, r.text)
	}
	return out
}

// maxKeyPattern is the longest key pattern of a rule, in bytes.
const maxKeyPattern = 1 << 10

// maxRules is the largest number of command rules, key patterns and selectors
// that one user can have.
const maxRules = 4096

// apply changes the set with one rule that is about keys or commands. It reports
// whether the token was such a rule.
func (p *permSet) apply(tok string, o ParseOptions) (bool, error) {
	if len(p.rules)+len(p.keys) >= maxRules {
		return true, errors.New("too many rules")
	}
	switch {
	case tok == "allkeys":
		p.keys = append(p.keys, keyRule{read: true, write: true, pattern: "*"})
	case tok == "resetkeys":
		p.keys = nil
	case strings.HasPrefix(tok, "~"):
		return true, p.addKey(tok[1:], true, true)
	case strings.HasPrefix(tok, "%"):
		return true, p.addKeyPrefixed(tok)
	case tok == "allcommands" || tok == "+@all":
		p.rules = append(p.rules, commandRule{allow: true, all: true, text: "+@all"})
	case tok == "nocommands" || tok == "-@all":
		p.rules = append(p.rules, commandRule{allow: false, all: true, text: "-@all"})
	case strings.HasPrefix(tok, "+") || strings.HasPrefix(tok, "-"):
		return true, p.commandRule(tok, o)
	default:
		return false, nil
	}
	return true, nil
}

// addKey adds a key pattern with the access that read and write say.
func (p *permSet) addKey(pattern string, read, write bool) error {
	if pattern == "" || len(pattern) > maxKeyPattern {
		return errors.New("a key pattern must have 1 to 1024 bytes")
	}
	p.keys = append(p.keys, keyRule{read: read, write: write, pattern: pattern})
	return nil
}

// addKeyPrefixed adds the rule of a token like %R~pattern, %W~pattern or
// %RW~pattern.
func (p *permSet) addKeyPrefixed(tok string) error {
	mode, pattern, ok := strings.Cut(tok[1:], "~")
	if !ok {
		return fmt.Errorf("bad key pattern %q", printable(tok))
	}
	switch strings.ToUpper(mode) {
	case "R":
		return p.addKey(pattern, true, false)
	case "W":
		return p.addKey(pattern, false, true)
	case "RW", "WR":
		return p.addKey(pattern, true, true)
	}
	return fmt.Errorf("bad key pattern %q", printable(tok))
}

// commandRule adds the rule of a token that starts with + or -.
func (p *permSet) commandRule(tok string, o ParseOptions) error {
	allow := tok[0] == '+'
	name := tok[1:]
	if strings.HasPrefix(name, "@") {
		cat, ok := ParseCategory(name[1:])
		if !ok {
			return fmt.Errorf("unknown category %q", printable(name))
		}
		p.rules = append(p.rules, commandRule{allow: allow, category: cat, text: string(tok[0]) + "@" + strings.ToLower(name[1:])})
		return nil
	}
	name = strings.ToLower(name)
	command, sub, hasSub := strings.Cut(name, "|")
	if command == "" || (hasSub && (sub == "" || strings.Contains(sub, "|"))) {
		return fmt.Errorf("unknown command %q", printable(name))
	}
	if o.KnownCommand != nil && !o.KnownCommand(command) {
		return fmt.Errorf("unknown command %q", printable(name))
	}
	if hasSub && o.KnownSub != nil && !o.KnownSub(command, sub) {
		return fmt.Errorf("unknown subcommand %q", printable(name))
	}
	p.rules = append(p.rules, commandRule{allow: allow, command: command, sub: sub, text: string(tok[0]) + name})
	return nil
}

// User is a user of the server.
type User struct {
	name      string
	enabled   bool
	noPass    bool
	certOnly  bool
	passwords []password
	root      permSet
	selectors []permSet
}

// Name returns the name of the user.
func (u *User) Name() string {
	return u.name
}

// Enabled reports whether the user can sign in.
func (u *User) Enabled() bool {
	return u.enabled
}

// clone returns a copy of u that shares nothing that a change of the copy can
// reach.
func (u *User) clone() *User {
	c := *u
	c.passwords = slices.Clone(u.passwords)
	c.root = permSet{rules: slices.Clone(u.root.rules), keys: slices.Clone(u.root.keys)}
	c.selectors = make([]permSet, len(u.selectors))
	for i, s := range u.selectors {
		c.selectors[i] = permSet{rules: slices.Clone(s.rules), keys: slices.Clone(s.keys)}
	}
	return &c
}

// allowed reports whether the user can run the command on the keys. The rules of
// the line and the selectors are alternatives.
func (u *User) allowed(command, sub string, cats Category, keys [][]byte) bool {
	if !u.enabled {
		return false
	}
	if u.root.allows(command, sub, cats, keys) {
		return true
	}
	for i := range u.selectors {
		if u.selectors[i].allows(command, sub, cats, keys) {
			return true
		}
	}
	return false
}

// allKeys reports whether the user can read every key, or write every key if write
// is true.
func (u *User) allKeys(write bool) bool {
	if !u.enabled {
		return false
	}
	if u.root.allKeys(write) {
		return true
	}
	for i := range u.selectors {
		if u.selectors[i].allKeys(write) {
			return true
		}
	}
	return false
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
	if u.certOnly {
		b.WriteString(" certonly")
	}
	for _, p := range u.passwords {
		b.WriteString(" ")
		b.WriteString(p.String())
	}
	for _, w := range u.root.text() {
		b.WriteString(" ")
		b.WriteString(w)
	}
	for i := range u.selectors {
		b.WriteString(" (")
		b.WriteString(strings.Join(u.selectors[i].text(), " "))
		b.WriteString(")")
	}
	return b.String()
}

// ParseOptions changes how rules are read.
type ParseOptions struct {
	// KnownCommand reports whether the server has a command. If it is nil, any
	// command name is accepted.
	KnownCommand func(name string) bool

	// KnownSub reports whether a command has a subcommand. If it is nil, any
	// subcommand name is accepted.
	KnownSub func(command, sub string) bool
}

// maxSelectors is the largest number of selectors of a user.
const maxSelectors = 64

// applyTokens changes u with the rules in toks, in order. A selector is a token
// that starts with ( and ends with ), or a run of tokens from one that starts with
// ( to one that ends with ). It returns whether a rule had a plain password, so
// that the caller can warn about it.
func (u *User) applyTokens(toks []string, o ParseOptions) (plain bool, err error) {
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		if strings.HasPrefix(tok, "(") {
			joined := tok
			for !strings.HasSuffix(joined, ")") {
				i++
				if i >= len(toks) {
					return plain, errors.New("a selector is not closed")
				}
				joined += " " + toks[i]
			}
			if err := u.addSelector(joined[1:len(joined)-1], o); err != nil {
				return plain, err
			}
			continue
		}
		p, err := u.applyRule(tok, o)
		if err != nil {
			return plain, err
		}
		plain = plain || p
	}
	return plain, nil
}

// addSelector adds the selector with the rules in text.
func (u *User) addSelector(text string, o ParseOptions) error {
	if len(u.selectors) >= maxSelectors {
		return errors.New("too many selectors")
	}
	var set permSet
	for _, tok := range strings.Fields(text) {
		ok, err := set.apply(tok, o)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("rule %q is not allowed in a selector", printable(tok))
		}
	}
	u.selectors = append(u.selectors, set)
	return nil
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
		u.certOnly = false
		u.passwords = nil
	case tok == "certonly":
		u.certOnly = true
		u.noPass = false
		u.passwords = nil
	case tok == "resetpass":
		u.noPass = false
		u.certOnly = false
		u.passwords = nil
	case tok == "reset":
		*u = User{name: u.name}
	case tok == "clearselectors":
		u.selectors = nil
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
		u.certOnly = false
		u.passwords = append(u.passwords, p)
		return true, nil
	case strings.HasPrefix(tok, "#") || strings.HasPrefix(tok, "$"):
		p, err := parsePassword(tok)
		if err != nil {
			return false, err
		}
		u.noPass = false
		u.certOnly = false
		u.passwords = append(u.passwords, p)
	case strings.HasPrefix(tok, "&") || tok == "allchannels" || tok == "resetchannels":
	default:
		ok, err := u.root.apply(tok, o)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, fmt.Errorf("unknown rule %q", printable(tok))
		}
	}
	return false, nil
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
