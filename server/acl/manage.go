// SPDX-License-Identifier: MIT

package acl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// copyUsers returns a copy of the map of users, so that a change does not touch
// the map that readers hold.
func (r *Registry) copyUsers() map[string]*User {
	out := map[string]*User{}
	if m := r.users.Load(); m != nil {
		for k, v := range *m {
			out[k] = v
		}
	}
	return out
}

// maxUsers is the largest number of users.
const maxUsers = 10000

// maxUserName is the longest name of a user, in bytes.
const maxUserName = 128

// validUserName reports whether name can be the name of a user: printable ASCII with
// no space.
func validUserName(name string) bool {
	if name == "" || len(name) > maxUserName {
		return false
	}
	for i := range len(name) {
		if name[i] <= 0x20 || name[i] > 0x7e {
			return false
		}
	}
	return true
}

// SetUser applies the rules to the user, and makes the user if it does not exist.
// The change is whole or none: if a rule is not valid, the user stays as it was. It
// returns the warnings of the rules.
func (r *Registry) SetUser(name string, rules []string) ([]string, error) {
	if !validUserName(name) {
		return nil, errors.New("a user name has 1 to 128 printable bytes and no space")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	users := r.copyUsers()
	u := &User{name: name}
	if old := users[name]; old != nil {
		u = old.clone()
	} else if len(users) >= maxUsers {
		return nil, errors.New("too many users")
	}
	plain, err := u.applyTokens(rules, r.opts)
	if err != nil {
		return nil, err
	}
	users[name] = u
	r.users.Store(&users)
	var warnings []string
	if plain {
		warnings = append(warnings, fmt.Sprintf("user %q was given a plain password, which the server hashed", printable(name)))
	}
	return warnings, nil
}

// DelUser removes the users and returns how many it removed. It refuses to remove
// the default user.
func (r *Registry) DelUser(names []string) (int, error) {
	if slices.Contains(names, "default") {
		return 0, errors.New("the default user cannot be removed")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	users := r.copyUsers()
	n := 0
	for _, name := range names {
		if _, ok := users[name]; ok {
			delete(users, name)
			n++
		}
	}
	r.users.Store(&users)
	return n, nil
}

// UserInfo is what ACL GETUSER shows about a user.
type UserInfo struct {
	// Flags are on or off, and nopass or certonly.
	Flags []string

	// Passwords are the hashes of the passwords.
	Passwords []string

	// Commands, Keys and Selectors are the rules, in the text of the ACL file.
	Commands, Keys string

	// Selectors are the rules of each selector, as Commands and Keys.
	Selectors []SelectorInfo
}

// SelectorInfo is the rules of one selector.
type SelectorInfo struct {
	// Commands and Keys are the rules, in the text of the ACL file.
	Commands, Keys string
}

// join returns the words of the rules of the commands of p, and of its keys.
func (p *permSet) join() (commands, keys string) {
	var c, k []string
	for _, r := range p.rules {
		c = append(c, r.text)
	}
	for _, r := range p.keys {
		k = append(k, r.text())
	}
	return strings.Join(c, " "), strings.Join(k, " ")
}

// Info returns what ACL GETUSER shows about the user, and false if there is no
// such user.
func (r *Registry) Info(name string) (UserInfo, bool) {
	u := r.get(name)
	if u == nil {
		return UserInfo{}, false
	}
	var info UserInfo
	if u.enabled {
		info.Flags = append(info.Flags, "on")
	} else {
		info.Flags = append(info.Flags, "off")
	}
	if u.noPass {
		info.Flags = append(info.Flags, "nopass")
	}
	if u.certOnly {
		info.Flags = append(info.Flags, "certonly")
	}
	for _, p := range u.passwords {
		info.Passwords = append(info.Passwords, p.String())
	}
	info.Commands, info.Keys = u.root.join()
	for i := range u.selectors {
		c, k := u.selectors[i].join()
		info.Selectors = append(info.Selectors, SelectorInfo{Commands: c, Keys: k})
	}
	return info, true
}

// Save writes the users to the ACL file. It writes a file next to it, with the
// mode 0600, and renames it, so the file is whole or unchanged.
func (r *Registry) Save() error {
	if r.path == "" {
		return errors.New("the server has no ACL file")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, line := range r.List() {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), filepath.Base(r.path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(0o600); err != nil {
			return fail(err)
		}
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, r.path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
