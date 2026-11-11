// SPDX-License-Identifier: MIT

//go:build !unix

package shale

import (
	"errors"
	"os"
)

// lockDir returns an error, because this system has no lock of directories.
func lockDir(dir string) (*os.File, error) {
	return nil, errors.ErrUnsupported
}

// unlockDir does nothing on this system.
func unlockDir(f *os.File) error {
	return nil
}
