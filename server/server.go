// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/command"
)

// ErrServerClosed is the error that Serve returns after Shutdown.
var ErrServerClosed = errors.New("server: closed")

// Server serves the commands over connections. Make one with New.
type Server struct {
	opts Options
	env  *command.Env
	reg  *command.Registry
	set  *command.Spec

	mu        sync.Mutex
	listeners map[net.Listener]struct{}
	conns     map[*conn]struct{}
	closing   atomic.Bool
	wg        sync.WaitGroup

	sweepOnce sync.Once
	sweepStop chan struct{}
	sweepDone chan struct{}

	started       time.Time
	clients       atomic.Int64
	totalConns    atomic.Uint64
	rejectedConns atomic.Uint64
	totalCommands atomic.Uint64
	protocolErrs  atomic.Uint64
	deniedCmds    atomic.Uint64
	readCalls     atomic.Uint64
	readBytes     atomic.Uint64
	setBatches    atomic.Uint64
	setBatched    atomic.Uint64
}

// New returns a Server with the options o.
func New(o Options) (*Server, error) {
	if o.Store == nil {
		return nil, errors.New("server: a store is required")
	}
	o = o.withDefaults()
	reg := command.NewRegistry()
	s := &Server{
		opts:      o,
		reg:       reg,
		set:       reg.Lookup([]byte("set")),
		listeners: make(map[net.Listener]struct{}),
		conns:     make(map[*conn]struct{}),
		started:   time.Now(),
	}
	s.env = &command.Env{
		Store:           o.Store,
		Locks:           command.NewLocks(),
		Access:          o.Access,
		Meta:            s,
		Registry:        s.reg,
		Log:             o.Log,
		Version:         o.Version,
		MaxKey:          o.MaxKey,
		MaxReply:        o.MaxReply,
		Now:             o.Now,
		AuthDelay:       o.AuthDelay,
		AuthDelayMax:    o.AuthDelayMax,
		MaxAuthFailures: o.MaxAuthFailures,
	}
	return s, nil
}

// Serve accepts connections on l and serves each one in its own goroutine. It
// returns ErrServerClosed after Shutdown, or the error of the listener. It
// closes l when it returns.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closing.Load() {
		s.mu.Unlock()
		_ = l.Close()
		return ErrServerClosed
	}
	s.listeners[l] = struct{}{}
	s.mu.Unlock()
	s.startSweeper()
	defer func() {
		s.mu.Lock()
		delete(s.listeners, l)
		s.mu.Unlock()
		_ = l.Close()
	}()

	backoff := time.Duration(0)
	for {
		nc, err := l.Accept()
		if err != nil {
			if s.closing.Load() || errors.Is(err, net.ErrClosed) {
				return ErrServerClosed
			}
			var ne net.Error
			if errors.As(err, &ne) {
				backoff = min(max(backoff*2, 5*time.Millisecond), time.Second)
				s.opts.Log.Warn("accept failed", "err", err, "retry_in", backoff)
				time.Sleep(backoff)
				continue
			}
			return err
		}
		backoff = 0
		s.accept(nc)
	}
}

// accept starts the goroutine of nc, or refuses it if there are too many
// clients.
func (s *Server) accept(nc net.Conn) {
	s.totalConns.Add(1)
	if s.clients.Add(1) > int64(s.opts.MaxClients) {
		s.clients.Add(-1)
		s.rejectedConns.Add(1)
		_ = nc.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = nc.Write([]byte("-ERR max number of clients reached\r\n"))
		_ = nc.Close()
		return
	}
	c := s.newConn(nc)
	s.mu.Lock()
	if s.closing.Load() {
		s.mu.Unlock()
		s.clients.Add(-1)
		_ = nc.Close()
		return
	}
	s.conns[c] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer s.clients.Add(-1)
		c.serve()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
}

// Shutdown stops the server. It closes the listeners, lets each connection
// finish the command that it runs and flush its replies, and waits for them. If
// ctx ends first, it closes the connections that remain and returns the error of
// ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopSweeper()
	s.mu.Lock()
	s.closing.Store(true)
	for l := range s.listeners {
		_ = l.Close()
	}
	for c := range s.conns {
		_ = c.nc.SetReadDeadline(time.Now())
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		for c := range s.conns {
			_ = c.nc.Close()
		}
		s.mu.Unlock()
		<-done
		return ctx.Err()
	}
}

// TLSListener returns a listener that serves TLS on l with the configuration
// cfg. The handshake happens in the goroutine of the connection, with a
// timeout, so a slow client cannot block Accept.
func TLSListener(l net.Listener, cfg *tls.Config) net.Listener {
	return tls.NewListener(l, cfg)
}

// limitsFor returns the limits for a client that has signed in or not.
func (s *Server) limitsFor(authed bool) resp.Limits {
	if authed {
		return s.opts.Limits
	}
	return s.opts.PreAuthLimits
}

// startSweeper starts the goroutine that removes the elements of dead collections.
// It starts one time, with the first listener, and it does not start if the
// interval is negative.
func (s *Server) startSweeper() {
	s.sweepOnce.Do(func() {
		if s.opts.SweepInterval <= 0 {
			return
		}
		s.sweepStop = make(chan struct{})
		s.sweepDone = make(chan struct{})
		go func() {
			defer close(s.sweepDone)
			s.env.RunSweeper(s.sweepStop, s.opts.SweepInterval)
		}()
	})
}

// stopSweeper stops the sweeper and waits for its pass to end.
func (s *Server) stopSweeper() {
	s.sweepOnce.Do(func() {})
	if s.sweepStop == nil {
		return
	}
	select {
	case <-s.sweepStop:
	default:
		close(s.sweepStop)
	}
	<-s.sweepDone
}
