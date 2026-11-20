// SPDX-License-Identifier: MIT

package command

import (
	"log/slog"
	"slices"
	"sync"
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

	// Subs are the lower case names of the subcommands, for a command whose first
	// argument names one. An ACL rule can allow or deny a single subcommand.
	Subs []string

	// WritesAllKeys is true for a command that changes every key without naming
	// them, such as FLUSHALL. A user can run it only if it can write every key.
	WritesAllKeys bool

	// ListsKeys is true for a command that returns key names that it does not take
	// as arguments, such as KEYS. A user can run it only if it can read every key.
	ListsKeys bool

	// Tx says how the command behaves between MULTI and EXEC.
	Tx TxMode

	// Handler runs the command.
	Handler Handler

	// Slot is the position of the command in Registry.Specs. NewRegistry sets it,
	// so that the server can keep a count for each command in a slice.
	Slot int
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

	// Checkpoint makes a copy of the data in a new directory.
	Checkpoint(dir string) (shale.CheckpointInfo, error)

	// Snapshot returns a view of the keys as of now. The caller must release it.
	Snapshot() (*shale.Snapshot, error)

	// Metrics returns the counts of the storage.
	Metrics() shale.Metrics
}

// Access decides who can sign in and what a user can run.
type Access interface {
	// RequireAuth reports whether a client must sign in before it runs commands.
	RequireAuth() bool

	// Authenticate reports whether the user and the password are valid.
	Authenticate(user string, password []byte) bool

	// Allowed reports whether the user can run the command, with its subcommand
	// if it has one, on the keys.
	Allowed(user, command, sub string, categories acl.Category, keys [][]byte) bool

	// AllKeysReadable reports whether the user can read every key.
	AllKeysReadable(user string) bool

	// AllKeysWritable reports whether the user can write every key.
	AllKeysWritable(user string) bool

	// Active reports whether the user exists and is on.
	Active(user string) bool

	// CertUser reports whether the user signs in with the client certificate whose
	// common name is the name of the user.
	CertUser(user string) bool

	// Record keeps a denied command or a failed sign in for ACL LOG.
	Record(reason, context, object, user, client string)

	// LogEntries returns up to n entries of ACL LOG, the newest first, or all of
	// them if n is negative.
	LogEntries(n int) []acl.LogEntry

	// LogReset removes the entries of ACL LOG.
	LogReset()

	// SetUser applies the rules to a user, and makes the user if it is new. It
	// returns the warnings.
	SetUser(name string, rules []string) ([]string, error)

	// DelUser removes users and returns how many it removed.
	DelUser(names []string) (int, error)

	// Info returns the rules of a user for ACL GETUSER.
	Info(name string) (acl.UserInfo, bool)

	// Save writes the users to the ACL file.
	Save() error

	// Users returns the names of the users.
	Users() []string

	// List returns each user as a line of the ACL file.
	List() []string

	// Reload reads the users again from their file, and returns the warnings.
	Reload() ([]string, error)
}

// AuthLimiter limits the failed sign ins that come from one address.
type AuthLimiter interface {
	// Blocked reports whether the address has too many failed sign ins, and the
	// server must refuse its sign ins without checking the password.
	Blocked(addr string) bool

	// Failed counts a failed sign in of the address.
	Failed(addr string)
}

// Operator gives the commands of an operator the list of the clients and the slow
// log of the server.
type Operator interface {
	// Clients returns a copy of the state of each client.
	Clients() []ClientInfo

	// Kill closes the connection of the client with id. It returns false if there
	// is no such client.
	Kill(id uint64) bool

	// CloseWhere closes the connection of each client, except the one with id
	// skip, for which keep returns false for the name of its user. It returns how
	// many it closed.
	CloseWhere(keep func(user string) bool, skip uint64) int

	// SlowLog returns up to n entries of the slow log, the newest first. If n is
	// negative it returns all of them.
	SlowLog(n int) []SlowEntry

	// SlowLogLen returns the number of entries of the slow log.
	SlowLogLen() int

	// SlowLogReset removes every entry of the slow log.
	SlowLogReset()
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

	// Operator gives CLIENT LIST, CLIENT KILL and SLOWLOG their data. It can be
	// nil, and then these commands are errors.
	Operator Operator

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

	// Health returns the error that stopped the storage, or nil. It can be nil,
	// and then the storage is always healthy.
	Health func() error

	// AuthLimit limits the failed sign ins of each address. It can be nil, and
	// then there is no limit by address.
	AuthLimit AuthLimiter

	nextID atomic.Uint64

	// Backup runs BGSAVE. It can be nil, and then BGSAVE is an error.
	Backup *Backup

	// Versions counts the writes of each key for WATCH. The server wraps Store
	// with it. It can be nil, and then WATCH never sees a change.
	Versions *Versions

	// MaxTxCommands is the largest number of commands that a transaction can
	// queue, and MaxTxBytes the largest total size of their arguments. MaxWatch is
	// the largest number of keys that a client can watch. A value that is not
	// positive is no limit.
	MaxTxCommands, MaxTxBytes, MaxWatch int

	uidMu    sync.Mutex
	uidNext  uint64
	uidLimit uint64
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

	// LAddr is the local address.
	LAddr string

	mu     sync.Mutex
	cmd    atomic.Pointer[string]
	active atomic.Int64

	scan scanState
	tx   txState
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
	for _, list := range [][]*Spec{stringCommands(), counterCommands(), expireCommands(), keyspaceCommands(), hashCommands(), setCommands(), moreStringCommands(), txCommands(), persistenceCommands(), flushCommands(), connectionCommands(), operatorCommands(), metaCommands()} {
		for _, s := range list {
			r.specs[s.Name] = s
		}
	}
	for i, s := range r.Specs() {
		s.Slot = i
	}
	return r
}
