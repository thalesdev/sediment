// SPDX-License-Identifier: MIT

package server

import (
	"sync"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/command"
)

// slowLog is a ring of the slowest commands, the newest last.
type slowLog struct {
	mu      sync.Mutex
	max     int
	nextID  uint64
	entries []command.SlowEntry
}

// add keeps an entry, and drops the oldest one if the log is full.
func (l *slowLog) add(e command.SlowEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.ID = l.nextID
	l.nextID++
	if len(l.entries) >= l.max {
		copy(l.entries, l.entries[1:])
		l.entries = l.entries[:len(l.entries)-1]
	}
	l.entries = append(l.entries, e)
}

// Clients returns a copy of the state of each client, oldest connection first.
func (s *Server) Clients() []command.ClientInfo {
	s.mu.Lock()
	out := make([]command.ClientInfo, 0, len(s.conns))
	for c := range s.conns {
		out = append(out, c.client.Info())
	}
	s.mu.Unlock()
	sortClients(out)
	return out
}

// sortClients sorts infos by client id.
func sortClients(infos []command.ClientInfo) {
	for i := 1; i < len(infos); i++ {
		for j := i; j > 0 && infos[j].ID < infos[j-1].ID; j-- {
			infos[j], infos[j-1] = infos[j-1], infos[j]
		}
	}
}

// CloseWhere closes the connection of each client, except the one with id skip,
// for which keep returns false for the name of its user.
func (s *Server) CloseWhere(keep func(user string) bool, skip uint64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for c := range s.conns {
		if c.client.ID == skip || keep(c.client.Info().User) {
			continue
		}
		_ = c.nc.Close()
		n++
	}
	return n
}

// Kill closes the connection of the client with id.
func (s *Server) Kill(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		if c.client.ID == id {
			_ = c.nc.Close()
			return true
		}
	}
	return false
}

// SlowLog returns up to n entries of the slow log, the newest first.
func (s *Server) SlowLog(n int) []command.SlowEntry {
	s.slow.mu.Lock()
	defer s.slow.mu.Unlock()
	total := len(s.slow.entries)
	if n < 0 || n > total {
		n = total
	}
	out := make([]command.SlowEntry, 0, n)
	for i := total - 1; i >= total-n; i-- {
		out = append(out, s.slow.entries[i])
	}
	return out
}

// SlowLogLen returns the number of entries of the slow log.
func (s *Server) SlowLogLen() int {
	s.slow.mu.Lock()
	defer s.slow.mu.Unlock()
	return len(s.slow.entries)
}

// SlowLogReset removes every entry of the slow log.
func (s *Server) SlowLogReset() {
	s.slow.mu.Lock()
	s.slow.entries = s.slow.entries[:0]
	s.slow.mu.Unlock()
}

// observe records that the client ran the command name with args arguments after the
// name from start to now. It is the one place that every command passes, and the
// place for the slow log, and later for metrics and traces.
func (s *Server) observe(c *command.Client, spec *command.Spec, n int, args int, kind resp.Kind, start time.Time) {
	took := time.Since(start)
	if s.metrics != nil {
		s.metrics.record(spec, int64(n), kind, took)
	}
	name := &spec.Name
	if limit := s.opts.SlowLogThreshold; limit >= 0 && took >= limit {
		s.slow.add(command.SlowEntry{At: start, Took: took, Command: *name, Args: args, Addr: c.Addr, Name: c.Info().Name})
		s.opts.Log.Warn("slow command", "client", c.ID, "addr", c.Addr, "user", c.User, "command", *name, "us", took.Microseconds())
	}
}
