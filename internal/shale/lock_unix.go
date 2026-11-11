// SPDX-License-Identifier: MIT

//go:build unix

package shale

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockDir takes an exclusive lock on the file LOCK in dir and returns the open
// file. It returns an error that wraps ErrLocked if another open file holds the
// lock. The lock belongs to the open file, so a second call in the same
// process also fails, and the system frees the lock when the process dies.
func lockDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
		}
		return nil, err
	}
	return f, nil
}

// unlockDir frees the lock that lockDir took and closes the file.
func unlockDir(f *os.File) error {
	unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
