// SPDX-License-Identifier: MIT

package shale

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/thalesdev/sediment/internal/manifest"
)

// CheckpointInfo describes a checkpoint.
type CheckpointInfo struct {
	// Seq is the largest sequence number in the checkpoint. It holds every write
	// that returned before Checkpoint was called, and a consistent prefix of the
	// writes that came after.
	Seq uint64

	// Tables is the number of table files.
	Tables int

	// Bytes is the size of the table files, which are hard links to the files of
	// the DB when the file system allows it, and copies otherwise.
	Bytes uint64
}

// Flush writes the memtable to a table, and returns when the table is in the
// MANIFEST. It takes the place of the leader of the writes, so the writes wait
// while it runs, and it does not return before the flush that it started ends.
func (db *DB) Flush() error {
	w := writerPool.Get().(*writer)
	*w = writer{barrier: true}
	defer func() {
		*w = writer{}
		writerPool.Put(w)
	}()
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed.Load() {
		return ErrClosed
	}
	db.queue = append(db.queue, w)
	for db.queue[0] != w {
		db.writeCond.Wait()
	}
	err := db.freezeAndWait()
	db.finish(1, err)
	return err
}

// freezeAndWait freezes the memtable if it has data, and waits until the flush has
// written it. The caller must hold db.mu and lead the writes.
func (db *DB) freezeAndWait() error {
	for {
		if db.closed.Load() {
			return ErrClosed
		}
		if db.err != nil {
			return db.err
		}
		state := db.state.Load()
		if state.imm != nil {
			db.stallFlush.wait(db.cond)
			continue
		}
		if state.mem.empty() {
			return nil
		}
		if err := db.freeze(state); err != nil {
			return db.fail("memtable switch", err)
		}
	}
}

// Checkpoint makes a copy of the DB in dir that Open can open, with the writes
// that returned before the call. It writes the memtable to a table, links the
// table files into a new directory, and writes a MANIFEST for them. The directory
// does not exist before the call, and it appears with all its files or not at all:
// the files go into dir.tmp, and a rename publishes them. A checkpoint does not
// copy the log, so it is as large as the tables. The DB keeps taking writes while
// the files are linked, and a compaction cannot remove a file that the checkpoint
// reads. A table file that the compaction replaced stays on disk until the
// checkpoint ends.
func (db *DB) Checkpoint(dir string) (CheckpointInfo, error) {
	dir = filepath.Clean(dir)
	if _, err := os.Stat(dir); err == nil {
		return CheckpointInfo{}, fmt.Errorf("shale: checkpoint directory %s exists", dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return CheckpointInfo{}, fmt.Errorf("shale: checkpoint: %w", err)
	}
	if err := db.Flush(); err != nil {
		return CheckpointInfo{}, err
	}
	state, _, err := db.pin()
	if err != nil {
		return CheckpointInfo{}, err
	}
	defer state.release()
	tmp := dir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return CheckpointInfo{}, fmt.Errorf("shale: checkpoint: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return CheckpointInfo{}, fmt.Errorf("shale: checkpoint: %w", err)
	}
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return CheckpointInfo{}, fmt.Errorf("shale: checkpoint: %w", err)
	}
	info, err := db.fillCheckpoint(tmp, state)
	if err == nil {
		if err = os.Rename(tmp, dir); err == nil {
			err = syncDir(filepath.Dir(dir))
		}
	}
	if err != nil {
		os.RemoveAll(tmp)
		return CheckpointInfo{}, fmt.Errorf("shale: checkpoint: %w", err)
	}
	return info, nil
}

// fillCheckpoint links the tables of the state into dir and writes the MANIFEST
// that names them.
func (db *DB) fillCheckpoint(dir string, state *readState) (CheckpointInfo, error) {
	var info CheckpointInfo
	var version manifest.Version
	metas := make([]manifest.TableMeta, 0, len(state.tables))
	var next uint64 = 1
	for _, t := range state.tables {
		if err := linkOrCopy(t.path, filepath.Join(dir, tableName(t.meta.Number))); err != nil {
			return CheckpointInfo{}, err
		}
		metas = append(metas, t.meta)
		info.Seq = max(info.Seq, t.meta.LargestSeq)
		info.Bytes += t.meta.Size
		next = max(next, t.meta.Number+1)
	}
	info.Tables = len(metas)
	if db.opts.checkpointHook != nil {
		if err := db.opts.checkpointHook("linked"); err != nil {
			return CheckpointInfo{}, err
		}
	}
	slices.SortFunc(metas, func(a, b manifest.TableMeta) int {
		switch {
		case a.Number < b.Number:
			return -1
		case a.Number > b.Number:
			return 1
		}
		return 0
	})
	version.Tables[0] = metas
	version.LastSeq = info.Seq
	version.NextFileNumber = next
	version.LogNumber = next
	f, _, err := writeManifestTo(dir, version)
	if err != nil {
		return CheckpointInfo{}, err
	}
	if err := f.Close(); err != nil {
		return CheckpointInfo{}, err
	}
	if db.opts.checkpointHook != nil {
		if err := db.opts.checkpointHook("manifest"); err != nil {
			return CheckpointInfo{}, err
		}
	}
	if err := syncDir(dir); err != nil {
		return CheckpointInfo{}, err
	}
	return info, nil
}

// linkOrCopy makes dst a hard link to src, and a copy of it if the file system
// does not allow a link, for example between two devices. A table file is
// immutable, so the two names can share it.
func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

// copyFile copies src to dst and flushes dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := (syncFile{out}).Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
