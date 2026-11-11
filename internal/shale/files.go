// SPDX-License-Identifier: MIT

package shale

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	lockName  = "LOCK"
	logSuffix = ".log"
	tabSuffix = ".sd"
	tmpSuffix = ".tmp"
	lostName  = "lost"
)

// logName returns the name of the log file with number n.
func logName(n uint64) string {
	return fmt.Sprintf("%06d%s", n, logSuffix)
}

// tableName returns the name of the table file with number n.
func tableName(n uint64) string {
	return fmt.Sprintf("%06d%s", n, tabSuffix)
}

// parseNumbered returns the number in a name like 000012.log. It accepts only
// the names that logName and tableName make.
func parseNumbered(name, suffix string) (uint64, bool) {
	stem, ok := strings.CutSuffix(name, suffix)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(stem, 10, 64)
	if err != nil || fmt.Sprintf("%06d%s", n, suffix) != name {
		return 0, false
	}
	return n, true
}

// listing is what a directory holds that the DB knows.
type listing struct {
	logs   []uint64
	tables []uint64
	tmps   []string
	max    uint64
}

// listDir reads dir and sorts the numbers. It ignores directories and names that
// the DB does not know. The number max is the largest file number in dir and in
// its lost directory.
func listDir(dir string) (listing, error) {
	var l listing
	entries, err := os.ReadDir(dir)
	if err != nil {
		return l, fmt.Errorf("shale: listing the directory: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if n, ok := parseNumbered(name, logSuffix); ok {
			l.logs = append(l.logs, n)
			l.max = max(l.max, n)
		} else if n, ok := parseNumbered(name, tabSuffix); ok {
			l.tables = append(l.tables, n)
			l.max = max(l.max, n)
		} else if strings.HasSuffix(name, tmpSuffix) {
			l.tmps = append(l.tmps, name)
		}
	}
	lost, err := os.ReadDir(filepath.Join(dir, lostName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return l, fmt.Errorf("shale: listing the lost directory: %w", err)
	}
	for _, e := range lost {
		if n, ok := parseNumbered(e.Name(), tabSuffix); ok {
			l.max = max(l.max, n)
		}
	}
	slices.Sort(l.logs)
	slices.Sort(l.tables)
	return l, nil
}

// listLogs returns the numbers of the log files in dir, in increasing order.
func listLogs(dir string) ([]uint64, error) {
	l, err := listDir(dir)
	return l.logs, err
}

// removeFile removes a file. It is not an error if the file does not exist.
func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("shale: removing %s: %w", filepath.Base(path), err)
	}
	return nil
}

// moveToLost moves the table files with the numbers to the lost directory of dir
// and flushes both directories. It returns an error if a file of the same name is
// already there, so that it never replaces a file.
func moveToLost(dir string, numbers []uint64) error {
	if len(numbers) == 0 {
		return nil
	}
	lost := filepath.Join(dir, lostName)
	if err := os.MkdirAll(lost, 0o755); err != nil {
		return fmt.Errorf("shale: creating the lost directory: %w", err)
	}
	for _, n := range numbers {
		dst := filepath.Join(lost, tableName(n))
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("shale: %s is already in the lost directory", tableName(n))
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("shale: moving %s: %w", tableName(n), err)
		}
		if err := os.Rename(filepath.Join(dir, tableName(n)), dst); err != nil {
			return fmt.Errorf("shale: moving %s: %w", tableName(n), err)
		}
	}
	if err := syncDir(lost); err != nil {
		return fmt.Errorf("shale: syncing the lost directory: %w", err)
	}
	return syncDir(dir)
}
