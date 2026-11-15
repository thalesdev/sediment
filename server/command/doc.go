// SPDX-License-Identifier: MIT

// Package command holds the commands of the server. A command does not know the
// network or the bytes of the protocol. It gets its arguments and the state of
// the client, and it returns a resp.Value, so another protocol can reuse it.
package command
