// SPDX-License-Identifier: MIT

package acl

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// maxLine is the longest line of an ACL file, in bytes.
const maxLine = 64 << 10

// Registry is the set of users. It is safe for concurrent use. Reload replaces
// all the users at once, so a reader sees the old set or the new one.
type Registry struct {
	path  string
	opts  ParseOptions
	users atomic.Pointer[map[string]*User]
	mu    sync.Mutex
	log   denyLog
}

// Load reads the users from the file at path. It also returns the warnings of the
// file, which are not errors. The file must not be readable by group or others,
// because it can hold password hashes. The users of the file are the only users:
// if it has no user named default, the default user is off.
func Load(path string, o ParseOptions) (*Registry, []string, error) {
	r := &Registry{path: path, opts: o}
	warnings, err := r.reload()
	if err != nil {
		return nil, nil, err
	}
	return r, warnings, nil
}

// Reload reads the file again, and replaces the users only if the whole file is
// valid. It returns the warnings of the file.
func (r *Registry) Reload() ([]string, error) {
	if r.path == "" {
		return nil, errors.New("the server has no ACL file")
	}
	return r.reload()
}

// reload reads the file and swaps the users.
func (r *Registry) reload() ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if runtime.GOOS != "windows" {
		st, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if st.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("the ACL file is readable by group or others, run chmod 600")
		}
	}
	users, warnings, err := Parse(f, r.opts)
	if err != nil {
		return nil, err
	}
	r.users.Store(&users)
	return warnings, nil
}

// Parse reads users from the text of an ACL file. A line is empty, a comment
// that starts with #, or "user <name> <rule>...".
func Parse(in io.Reader, o ParseOptions) (map[string]*User, []string, error) {
	users := map[string]*User{}
	var warnings []string
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 4096), maxLine)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		words := strings.Fields(line)
		if len(words) < 2 || words[0] != "user" {
			return nil, nil, fmt.Errorf("line %d: expected 'user <name> <rules>'", n)
		}
		name := words[1]
		if _, dup := users[name]; dup {
			return nil, nil, fmt.Errorf("line %d: user %q is defined twice", n, printable(name))
		}
		u := &User{name: name}
		plain, err := u.applyTokens(words[2:], o)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: user %q: %w", n, printable(name), err)
		}
		if plain {
			warnings = append(warnings, fmt.Sprintf("line %d: user %q has a plain password in the file, replace it with its hash", n, printable(name)))
		}
		users[name] = u
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if _, ok := users["default"]; !ok {
		users["default"] = &User{name: "default"}
	}
	return users, warnings, nil
}

// get returns the user with the name, or nil.
func (r *Registry) get(name string) *User {
	m := r.users.Load()
	if m == nil {
		return nil
	}
	return (*m)[name]
}

// RequireAuth reports whether a client must sign in. It is false only if the
// default user is on and has no password.
func (r *Registry) RequireAuth() bool {
	u := r.get("default")
	return u == nil || !u.enabled || !u.noPass
}

// Authenticate reports whether the user exists, is on, and has the password.
func (r *Registry) Authenticate(name string, pw []byte) bool {
	u := r.get(name)
	if u == nil || !u.enabled || u.certOnly {
		dummy.verify(pw)
		return false
	}
	if u.noPass {
		return true
	}
	if len(u.passwords) == 0 {
		dummy.verify(pw)
		return false
	}
	ok := false
	for _, p := range u.passwords {
		if p.verify(pw) {
			ok = true
		}
	}
	return ok
}

// Allowed reports whether the user can run the command, with its subcommand if it
// has one, on the keys. A command that writes needs the right to write each key,
// and any other command the right to read it.
func (r *Registry) Allowed(name, command, sub string, cats Category, keys [][]byte) bool {
	u := r.get(name)
	return u != nil && u.allowed(command, sub, cats, keys)
}

// AllKeysReadable reports whether the user can read every key. The commands that
// list keys, KEYS, SCAN and RANDOMKEY, need it.
func (r *Registry) AllKeysReadable(name string) bool {
	u := r.get(name)
	return u != nil && u.allKeys(false)
}

// AllKeysWritable reports whether the user can write every key. FLUSHALL needs it.
func (r *Registry) AllKeysWritable(name string) bool {
	u := r.get(name)
	return u != nil && u.allKeys(true)
}

// Active reports whether the user exists and is on.
func (r *Registry) Active(name string) bool {
	u := r.get(name)
	return u != nil && u.enabled
}

// CertUser reports whether the user exists, is on, and signs in with a client
// certificate whose common name is its name, which is what the rule certonly says.
func (r *Registry) CertUser(name string) bool {
	u := r.get(name)
	return u != nil && u.enabled && u.certOnly
}

// Users returns the names of the users, sorted.
func (r *Registry) Users() []string {
	m := r.users.Load()
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(*m))
	for name := range *m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// List returns each user as a line of the ACL file, sorted by name.
func (r *Registry) List() []string {
	names := r.Users()
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, r.get(name).describe())
	}
	return out
}
