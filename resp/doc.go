// SPDX-License-Identifier: MIT

// Package resp reads and writes the Redis serialization protocol, RESP2 and
// RESP3. It does not use the network and it does not know the database. It
// reads from an io.Reader and writes to an io.Writer.
//
// All the bytes that the Reader reads are not trusted. The Reader checks every
// length against Limits before it allocates, and it allocates in steps while
// the bytes arrive, so a client pays in bandwidth for each byte that the server
// allocates. A malformed input is a *ProtocolError, never a panic.
package resp
