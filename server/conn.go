// SPDX-License-Identifier: MIT

package server

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"runtime/debug"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/command"
)

// conn is one client connection.
type conn struct {
	s      *Server
	nc     net.Conn
	r      *resp.Reader
	w      *resp.Writer
	client *command.Client
}

// deadlineWriter sets the write deadline before each write, so a client that
// does not read cannot hold the goroutine for longer than WriteTimeout.
type deadlineWriter struct {
	c *conn
}

// Write writes p to the connection with a deadline.
func (d deadlineWriter) Write(p []byte) (int, error) {
	_ = d.c.nc.SetWriteDeadline(time.Now().Add(d.c.s.opts.WriteTimeout))
	return d.c.nc.Write(p)
}

// flushReader flushes the replies before it reads from the connection. The
// reader calls it only when it has no byte left, which is when the goroutine
// would wait for the client, so the replies of a pipeline leave in few writes and
// no reply waits while the server waits for the client.
type flushReader struct {
	c *conn
}

// Read flushes the replies and then reads from the connection.
func (f flushReader) Read(p []byte) (int, error) {
	if f.c.w.Buffered() > 0 {
		if err := f.c.w.Flush(); err != nil {
			return 0, err
		}
	}
	n, err := f.c.nc.Read(p)
	f.c.s.readCalls.Add(1)
	f.c.s.readBytes.Add(uint64(n))
	return n, err
}

// newConn returns the state for nc.
func (s *Server) newConn(nc net.Conn) *conn {
	c := &conn{s: s, nc: nc}
	authed := s.opts.Access == nil || !s.opts.Access.RequireAuth()
	c.r = resp.NewReader(flushReader{c}, s.limitsFor(authed))
	c.w = resp.NewWriter(deadlineWriter{c})
	c.client = &command.Client{
		ID:      s.env.NextID(),
		Addr:    nc.RemoteAddr().String(),
		LAddr:   nc.LocalAddr().String(),
		Proto:   2,
		Authed:  authed,
		User:    "default",
		Created: time.Now(),
	}
	return c
}

// serve runs the connection until it ends. A panic ends only this connection.
func (c *conn) serve() {
	defer func() { _ = c.nc.Close() }()
	c.s.opts.Log.Debug("connection opened", "client", c.client.ID, "addr", c.client.Addr)
	defer func() {
		c.s.opts.Log.Debug("connection closed", "client", c.client.ID, "addr", c.client.Addr, "user", printable([]byte(c.client.User), 64))
	}()
	defer func() {
		if r := recover(); r != nil {
			c.s.opts.Log.Error("connection panic", "client", c.client.ID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if tc, ok := c.nc.(*tls.Conn); ok {
		_ = tc.SetDeadline(time.Now().Add(c.s.opts.HandshakeTimeout))
		if err := tc.Handshake(); err != nil {
			c.s.opts.Log.Debug("tls handshake failed", "client", c.client.ID, "err", err)
			return
		}
		_ = tc.SetDeadline(time.Time{})
		c.certSignIn(tc)
	}
	c.loop()
	_ = c.w.Flush()
}

// loop reads and runs commands until the connection ends.
func (c *conn) loop() {
	o := &c.s.opts
	var carry [][]byte
	for {
		var args [][]byte
		if carry != nil {
			args, carry = carry, nil
		} else {
			if c.r.Buffered() == 0 {
				idle := o.IdleTimeout
				if !c.client.Authed && (idle <= 0 || o.PreAuthTimeout < idle) {
					idle = o.PreAuthTimeout
				}
				if idle > 0 {
					_ = c.nc.SetReadDeadline(time.Now().Add(idle))
				} else {
					_ = c.nc.SetReadDeadline(time.Time{})
				}
				if c.s.closing.Load() {
					return
				}
				if err := c.r.Wait(); err != nil {
					return
				}
				_ = c.nc.SetReadDeadline(time.Now().Add(o.ReadTimeout))
			}
			var err error
			if args, err = c.r.ReadCommand(); err != nil {
				c.readFailed(err)
				return
			}
		}
		if isPlainSet(args) && c.canBatchSet(args) {
			var readErr error
			var written bool
			carry, readErr, written = c.batchSets(args)
			if !written {
				return
			}
			if readErr != nil {
				_ = c.w.Flush()
				c.readFailed(readErr)
				return
			}
			continue
		}
		wasAuthed := c.client.Authed
		reply := c.execute(args)
		c.w.SetProtocol(c.client.Proto)
		if err := c.w.WriteValue(reply); err != nil {
			return
		}
		if c.client.Close {
			return
		}
		if !wasAuthed && c.client.Authed {
			c.r.SetLimits(c.s.limitsFor(true))
		} else if wasAuthed && !c.client.Authed {
			c.r.SetLimits(c.s.limitsFor(false))
		}
	}
}

// readFailed handles the error of a read. A protocol error gets a reply, except
// a cross protocol one, and then the connection closes.
func (c *conn) readFailed(err error) {
	if errors.Is(err, resp.ErrCrossProtocol) {
		c.s.protocolErrs.Add(1)
		c.s.opts.Log.Warn("closed a connection that sent an HTTP request", "client", c.client.ID, "addr", c.client.Addr)
		return
	}
	var pe *resp.ProtocolError
	if errors.As(err, &pe) {
		c.s.protocolErrs.Add(1)
		c.s.opts.Log.Debug("protocol error", "client", c.client.ID, "err", pe.Msg)
		_ = c.w.WriteValue(resp.Error("ERR Protocol error: " + pe.Msg))
		return
	}
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		c.s.opts.Log.Debug("read failed", "client", c.client.ID, "err", err)
	}
}

// certSignIn signs the client in as the user that the common name of its
// certificate names, if the server has that setting and the user has the rule
// certonly. The chain of the certificate was checked by the handshake.
func (c *conn) certSignIn(tc *tls.Conn) {
	a := c.s.opts.Access
	if !c.s.opts.CertUser || a == nil {
		return
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return
	}
	cn := certs[0].Subject.CommonName
	if !a.CertUser(cn) {
		c.s.opts.Log.Warn("a client certificate names no user that signs in with one", "addr", c.client.Addr, "cn", printable([]byte(cn), 64))
		return
	}
	c.client.SignInAs(cn)
	c.r.SetLimits(c.s.limitsFor(true))
	c.s.opts.Log.Info("signed in with a client certificate", "client", c.client.ID, "addr", c.client.Addr, "user", cn)
}
