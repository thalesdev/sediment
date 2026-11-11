// SPDX-License-Identifier: MIT

package shale

import "errors"

var (
	// ErrNotFound is the error of Get for a key that does not exist or that
	// was deleted.
	ErrNotFound = errors.New("shale: key not found")

	// ErrClosed is the error of each method of a DB after Close.
	ErrClosed = errors.New("shale: database closed")

	// ErrLocked is the error of Open for a directory that another DB has
	// open, in this process or in another one.
	ErrLocked = errors.New("shale: database directory is locked")

	errSeqExhausted = errors.New("shale: sequence numbers are exhausted")
)
