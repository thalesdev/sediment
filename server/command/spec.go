// SPDX-License-Identifier: MIT

package command

import (
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

// Handler runs a command. args[0] is the name that the client sent, and the
// arity of the command was already checked.
type Handler func(env *Env, c *Client, args [][]byte) resp.Value

// Spec describes a command.
type Spec struct {
	// Name is the lower case name of the command.
	Name string

	// Arity is the number of arguments, with the name. A positive number is an
	// exact count. A negative number means at least its absolute value.
	Arity int

	// Categories are the groups that the command belongs to.
	Categories acl.Category

	// NoAuth is true for the commands that a client can run before it signs in.
	NoAuth bool

	// FirstKey, LastKey and KeyStep tell where the keys are in the arguments, as
	// in the COMMAND reply. A command without keys has 0 in each.
	FirstKey, LastKey, KeyStep int

	// Handler runs the command.
	Handler Handler
}

// Store is the storage that the commands use. A *shale.DB is one.
type Store interface {
	// Get returns a copy of the value of key, or shale.ErrNotFound.
	Get(key []byte) ([]byte, error)

	// Put sets the value of key.
	Put(key, value []byte) error

	// Delete deletes key.
	Delete(key []byte) error

	// Write applies a batch as one unit.
	Write(b *shale.Batch) error

	// NewIterator returns an iterator over the keys, as of now. The caller must
	// close it.
	NewIterator(opts *shale.IterOptions) (shale.Iterator, error)

	// Metrics returns the counts of the storage.
	Metrics() shale.Metrics
}

// Access decides who can sign in and what a user can run.
type Access interface {
	// RequireAuth reports whether a client must sign in before it runs commands.
	RequireAuth() bool

	// Authenticate reports whether the user and the password are valid.
	Authenticate(user string, password []byte) bool

	// Allowed reports whether the user can run the command.
	Allowed(user, command string, categories acl.Category) bool

	// Users returns the names of the users.
	Users() []string

	// List returns each user as a line of the ACL file.
	List() []string

	// Reload reads the users again from their file, and returns the warnings.
	Reload() ([]string, error)
}

// Meta gives the commands the state of the server.
type Meta interface {
	// Info returns the text of the INFO reply for a section, or for all of them
	// if section is empty.
	Info(section string) string

	// Config returns the name and value of every setting whose name matches the
	// glob pattern.
	Config(pattern string) [][2]string
}

// Env is what the commands share. The server makes one for all its clients.
type Env struct {
	// Store is the storage.
	Store Store

	// Locks serializes the writes that share a key.
	Locks *Locks

	// Access is nil if the server has no users.
	Access Access

	// Meta gives INFO and CONFIG GET their data.
	Meta Meta

	// Registry holds the commands. COMMAND reads it.
	Registry *Registry

	// Log gets the errors of the storage. It never gets keys, values or
	// passwords.
	Log *slog.Logger

	// Version is the version of the server.
	Version string

	// MaxKey is the largest size of a key, in bytes.
	MaxKey int

	// MaxReply is the largest total size of the values in one reply.
	MaxReply int

	// Now returns the time that expiry uses. If it is nil, the time of the
	// system is used.
	Now func() time.Time

	// AuthDelay is the wait after the first failed sign in. Each next failure
	// doubles it, up to AuthDelayMax.
	AuthDelay time.Duration

	// AuthDelayMax is the longest wait after a failed sign in.
	AuthDelayMax time.Duration

	// MaxAuthFailures is the number of failed sign ins after which the
	// connection is closed.
	MaxAuthFailures int

	nextID atomic.Uint64
}

// NextID returns a new client id.
func (e *Env) NextID() uint64 {
	return e.nextID.Add(1)
}

// Client is the state of one connection that the commands read and change.
type Client struct {
	// ID is the number of the client.
	ID uint64

	// Addr is the remote address.
	Addr string

	// Name is set by CLIENT SETNAME and HELLO SETNAME.
	Name string

	// LibName and LibVer are set by CLIENT SETINFO.
	LibName, LibVer string

	// User is the name of the user that signed in.
	User string

	// Proto is the version of the protocol, 2 or 3.
	Proto int

	// Authed is true if the client can run commands.
	Authed bool

	// Close is set by a command that wants the server to close the connection
	// after the reply.
	Close bool

	// AuthFailures counts the failed sign ins of this connection.
	AuthFailures int

	// Created is the time of the connection.
	Created time.Time

	scan scanState
}

// Registry is a set of commands.
type Registry struct {
	specs map[string]*Spec
}

// Lookup returns the command with the name, in any case, or nil.
func (r *Registry) Lookup(name []byte) *Spec {
	var buf [32]byte
	if len(name) > len(buf) {
		return nil
	}
	for i, c := range name {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf[i] = c
	}
	return r.specs[string(buf[:len(name)])]
}

// Specs returns all the commands, sorted by name.
func (r *Registry) Specs() []*Spec {
	out := make([]*Spec, 0, len(r.specs))
	for _, s := range r.specs {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *Spec) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return out
}

// NewRegistry returns a registry with all the commands of the server.
func NewRegistry() *Registry {
	r := &Registry{specs: make(map[string]*Spec)}
	for _, list := range [][]*Spec{stringCommands(), counterCommands(), expireCommands(), keyspaceCommands(), connectionCommands(), metaCommands()} {
		for _, s := range list {
			r.specs[s.Name] = s
		}
	}
	return r
}
