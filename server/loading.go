// SPDX-License-Identifier: MIT

package server

import (
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/thalesdev/sediment/resp"
)

// loadingTimeout is how long a connection of a loading server can wait for a
// command.
const loadingTimeout = 30 * time.Second

// Loading answers on the listeners while the DB opens and the server does not exist
// yet. INFO shows loading:1, and every other command gets a LOADING error, so a
// health check can tell a node that is starting from one that is down. Call Close
// before the real server takes the listeners.
type Loading struct {
	mu        sync.Mutex
	closed    bool
	listeners map[net.Listener]struct{}
	conns     map[net.Conn]struct{}
	wg        sync.WaitGroup
}

// NewLoading returns a Loading that serves no listener yet.
func NewLoading() *Loading {
	return &Loading{listeners: map[net.Listener]struct{}{}, conns: map[net.Conn]struct{}{}}
}

// Serve accepts connections on l until Close, and returns nil after it. It closes
// l when it returns.
func (s *Loading) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = l.Close()
		return nil
	}
	s.listeners[l] = struct{}{}
	s.mu.Unlock()
	defer l.Close()
	for {
		nc, err := l.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = nc.Close()
			return nil
		}
		s.conns[nc] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(nc)
	}
}

// serve answers the commands of one connection.
func (s *Loading) serve(nc net.Conn) {
	defer s.wg.Done()
	defer func() {
		_ = nc.Close()
		s.mu.Lock()
		delete(s.conns, nc)
		s.mu.Unlock()
	}()
	r := resp.NewReader(nc, preAuthLimits.WithDefaults())
	w := resp.NewWriter(nc)
	for {
		_ = nc.SetReadDeadline(time.Now().Add(loadingTimeout))
		args, err := r.ReadCommand()
		if err != nil {
			return
		}
		reply := resp.Error("LOADING sediment is loading the data set, try again soon")
		if strings.EqualFold(string(args[0]), "info") {
			reply = resp.Value{Kind: resp.KindVerbatim, Bytes: []byte("# Server\r\nloading:1\r\n"), Format: "txt"}
		}
		_ = nc.SetWriteDeadline(time.Now().Add(time.Second))
		if err := w.WriteValue(reply); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

// Close closes the listeners and the connections, and waits for the goroutines.
func (s *Loading) Close() {
	s.mu.Lock()
	s.closed = true
	for l := range s.listeners {
		_ = l.Close()
	}
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}
