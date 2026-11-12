// SPDX-License-Identifier: MIT

package shale

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/thalesdev/sediment/internal/manifest"
	"github.com/thalesdev/sediment/internal/wal"
)

const (
	manifestName    = "MANIFEST"
	manifestTmpName = "MANIFEST.tmp"
)

// readManifest reads the MANIFEST in dir and returns the version that its edits
// build, and the number of bytes that it skipped at the end. A cut or damaged end
// is the end of the file. If the file does not exist, it returns the empty
// version. It returns an error if the file has no valid edit, because a MANIFEST
// always starts with one edit that arrives whole.
func readManifest(dir string) (version manifest.Version, dropped int, err error) {
	f, err := os.Open(filepath.Join(dir, manifestName))
	if errors.Is(err, os.ErrNotExist) {
		return manifest.Version{}, 0, nil
	}
	if err != nil {
		return manifest.Version{}, 0, fmt.Errorf("shale: opening the MANIFEST: %w", err)
	}
	defer f.Close()
	r := wal.NewReader(f)
	edits := 0
	for {
		record, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return manifest.Version{}, 0, fmt.Errorf("shale: reading the MANIFEST: %w", err)
		}
		edit, err := manifest.Decode(record)
		if err != nil {
			return manifest.Version{}, 0, fmt.Errorf("shale: MANIFEST: %w", err)
		}
		if err := version.Apply(edit); err != nil {
			return manifest.Version{}, 0, fmt.Errorf("shale: MANIFEST: %w", err)
		}
		edits++
	}
	if edits == 0 {
		return manifest.Version{}, 0, fmt.Errorf("shale: the MANIFEST has no valid edit: %w", manifest.ErrCorrupt)
	}
	return version, r.Dropped(), nil
}

// writeManifest writes version as one edit to a new MANIFEST. It writes a
// temporary file, flushes it, renames it over the MANIFEST and flushes the
// directory. The rename is the commit. The DB keeps the file and the writer, so
// that later edits go on in the same log, with the blocks in line.
func (db *DB) writeManifest(version manifest.Version) error {
	tmp := filepath.Join(db.dir, manifestTmpName)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("shale: creating the MANIFEST: %w", err)
	}
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("shale: writing the MANIFEST: %w", err)
	}
	w := wal.NewWriter(syncFile{f})
	if err := w.AddRecord(version.Compact().Encode(nil)); err != nil {
		return fail(err)
	}
	if err := w.Sync(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, filepath.Join(db.dir, manifestName)); err != nil {
		return fail(err)
	}
	if err := syncDir(db.dir); err != nil {
		f.Close()
		return fmt.Errorf("shale: syncing the directory: %w", err)
	}
	db.manifest, db.manifestLog = f, w
	return nil
}

// appendEdit adds edit to the MANIFEST and flushes it. The edit is a commit when
// the flush returns. The flush and the compaction can call it at the same time,
// and it writes one edit at a time.
func (db *DB) appendEdit(edit manifest.Edit) error {
	db.manifestMu.Lock()
	defer db.manifestMu.Unlock()
	if err := db.manifestLog.AddRecord(edit.Encode(nil)); err != nil {
		return fmt.Errorf("shale: writing the MANIFEST: %w", err)
	}
	if err := db.manifestLog.Sync(); err != nil {
		return fmt.Errorf("shale: syncing the MANIFEST: %w", err)
	}
	return nil
}
