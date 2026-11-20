// SPDX-License-Identifier: MIT

package server

import (
	"net"
	"sync"
	"time"
)

// authLimit counts the failed sign ins of each address. After limit failures, each
// less than window after the one before, it refuses the address until window has
// passed since the last failure.
type authLimit struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu    sync.Mutex
	hosts map[string]*hostFails
	calls int
}

// hostFails is the count of one address.
type hostFails struct {
	count int
	last  time.Time
}

// maxTrackedHosts is the number of addresses after which a call removes the
// entries that expired, so that the table cannot grow without bound.
const maxTrackedHosts = 4096

// newAuthLimit returns a limit of limit failures in window, or nil if limit is
// not positive.
func newAuthLimit(limit int, window time.Duration, now func() time.Time) *authLimit {
	if limit <= 0 {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &authLimit{limit: limit, window: window, now: now, hosts: make(map[string]*hostFails)}
}

// hostOf returns the address without its port, so that a client cannot get a new
// count with a new connection.
func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

// Blocked reports whether the address is refused now.
func (a *authLimit) Blocked(addr string) bool {
	host := hostOf(addr)
	a.mu.Lock()
	defer a.mu.Unlock()
	h := a.hosts[host]
	if h == nil {
		return false
	}
	if a.now().Sub(h.last) >= a.window {
		delete(a.hosts, host)
		return false
	}
	return h.count >= a.limit
}

// Failed counts a failed sign in of the address.
func (a *authLimit) Failed(addr string) {
	host := hostOf(addr)
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.hosts) >= maxTrackedHosts {
		for k, h := range a.hosts {
			if now.Sub(h.last) >= a.window {
				delete(a.hosts, k)
			}
		}
	}
	h := a.hosts[host]
	if h == nil || now.Sub(h.last) >= a.window {
		h = &hostFails{}
		a.hosts[host] = h
	}
	h.count++
	h.last = now
}
