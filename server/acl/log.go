// SPDX-License-Identifier: MIT

package acl

import (
	"sync"
	"time"
)

// maxLogEntries is the number of entries that the denial log keeps.
const maxLogEntries = 128

// LogEntry is one denied command or failed sign in. It never holds a key, a value
// or a password.
type LogEntry struct {
	// ID is the number of the entry, which grows by one for each entry.
	ID uint64

	// Reason is command, key or auth.
	Reason string

	// Context is toplevel, or multi for a command that ran in EXEC.
	Context string

	// Object is the name of the command that was denied, or AUTH.
	Object string

	// User is the name of the user, as the client gave it for a failed sign in.
	User string

	// Client is the CLIENT INFO line of the client.
	Client string

	// At is the time of the entry.
	At time.Time
}

// denyLog is a ring of the last denials.
type denyLog struct {
	mu      sync.Mutex
	nextID  uint64
	entries []LogEntry
}

// Record keeps a denied command or a failed sign in. The oldest entry goes when
// the log is full.
func (r *Registry) Record(reason, context, object, user, client string) {
	l := &r.log
	l.mu.Lock()
	defer l.mu.Unlock()
	e := LogEntry{ID: l.nextID, Reason: reason, Context: context, Object: object, User: user, Client: client, At: time.Now()}
	l.nextID++
	if len(l.entries) >= maxLogEntries {
		copy(l.entries, l.entries[1:])
		l.entries = l.entries[:len(l.entries)-1]
	}
	l.entries = append(l.entries, e)
}

// LogEntries returns up to n entries, the newest first. A negative n returns all
// of them.
func (r *Registry) LogEntries(n int) []LogEntry {
	l := &r.log
	l.mu.Lock()
	defer l.mu.Unlock()
	total := len(l.entries)
	if n < 0 || n > total {
		n = total
	}
	out := make([]LogEntry, 0, n)
	for i := total - 1; i >= total-n; i-- {
		out = append(out, l.entries[i])
	}
	return out
}

// LogReset removes every entry.
func (r *Registry) LogReset() {
	l := &r.log
	l.mu.Lock()
	l.entries = l.entries[:0]
	l.mu.Unlock()
}
