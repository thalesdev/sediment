// SPDX-License-Identifier: MIT

// Package server is the network server of sediment. It accepts connections on a
// net.Listener, reads commands with package resp, runs them with package command
// and writes the replies. It does not know which transport carries the bytes, so
// a TLS listener or another kind of connection can take the place of plain TCP.
//
// Each connection has one goroutine. Every limit is checked before it costs
// memory or time: the size of what a client sends, the number of clients, the
// time that a client takes to send a command and the size of what waits to be
// sent to it. A client that has not signed in gets tighter limits.
package server
