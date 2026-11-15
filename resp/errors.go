// SPDX-License-Identifier: MIT

package resp

import "errors"

// ProtocolError is the error of a read of bytes that are not valid RESP or that
// break a limit. The connection is in an unknown state after it, and the caller
// must close it.
type ProtocolError struct {
	// Msg tells what is wrong. It is safe to send to the client.
	Msg string
}

// Error returns the message of the error.
func (e *ProtocolError) Error() string {
	return "resp: protocol error: " + e.Msg
}

// ErrCrossProtocol is the error of an inline command that starts like an HTTP
// request. A web page or a proxy that is made to send a request to this port
// can only send that. The caller must close the connection.
var ErrCrossProtocol = &ProtocolError{Msg: "possible cross protocol attack"}

// protocolError returns a *ProtocolError with the message msg.
func protocolError(msg string) error {
	return &ProtocolError{Msg: msg}
}

// IsProtocolError reports whether err is a *ProtocolError.
func IsProtocolError(err error) bool {
	var pe *ProtocolError
	return errors.As(err, &pe)
}
