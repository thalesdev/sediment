// SPDX-License-Identifier: MIT

package command

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

var (
	replyNoBackupDir = resp.Error("ERR no backup-dir is set")
	replySaveBusy    = resp.Error("ERR Background save already in progress")
)

// Backup runs the checkpoints that BGSAVE starts, one at a time, and keeps the
// result of the last one.
type Backup struct {
	// Dir is the directory that holds the checkpoints. A client cannot choose where
	// a checkpoint goes, only the settings can.
	Dir string

	mu           sync.Mutex
	running      bool
	wg           sync.WaitGroup
	lastSave     int64
	lastDuration time.Duration
	lastBytes    uint64
	lastErr      error
	lastName     string
}

// BackupState is a copy of the state of a Backup, for INFO.
type BackupState struct {
	Running      bool
	LastSave     int64
	LastDuration time.Duration
	LastBytes    uint64
	LastErr      error
	LastName     string
}

// State returns the state of the backups.
func (b *Backup) State() BackupState {
	if b == nil {
		return BackupState{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return BackupState{b.running, b.lastSave, b.lastDuration, b.lastBytes, b.lastErr, b.lastName}
}

// Wait waits for the checkpoint that runs, if there is one. The server calls it
// before the store closes.
func (b *Backup) Wait() {
	if b != nil {
		b.wg.Wait()
	}
}

// start marks a checkpoint as running. It returns false if one already runs.
func (b *Backup) start() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return false
	}
	b.running = true
	b.wg.Add(1)
	return true
}

// persistenceCommands returns BGSAVE and LASTSAVE.
func persistenceCommands() []*Spec {
	return []*Spec{
		{Name: "bgsave", Arity: 1, Categories: acl.CatAdmin | acl.CatSlow | acl.CatDangerous, Handler: bgsave},
		{Name: "lastsave", Arity: 1, Categories: acl.CatAdmin | acl.CatFast | acl.CatDangerous, Handler: lastsave},
	}
}

// checkpointPath returns the path of a new checkpoint in dir, named by the time.
func checkpointPath(dir string, now int64) string {
	name := fmt.Sprintf("backup-%d", now/1000)
	path := filepath.Join(dir, name)
	for n := 1; ; n++ {
		_, err := os.Stat(path)
		_, tmpErr := os.Stat(path + ".tmp")
		if err != nil && tmpErr != nil {
			return path
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d", name, n))
	}
}

// bgsave is BGSAVE. It starts a checkpoint of the store in the background and
// replies at once. The checkpoint goes into a new directory of the backup
// directory, and LASTSAVE and INFO tell how it ended.
func bgsave(env *Env, _ *Client, _ [][]byte) resp.Value {
	b := env.Backup
	if b == nil || b.Dir == "" {
		return replyNoBackupDir
	}
	if !b.start() {
		return replySaveBusy
	}
	path := checkpointPath(b.Dir, env.nowMillis())
	go func() {
		defer b.wg.Done()
		began := time.Now()
		info, err := env.Store.Checkpoint(path)
		took := time.Since(began)
		b.mu.Lock()
		b.running = false
		b.lastDuration = took
		b.lastErr = err
		if err == nil {
			b.lastSave = env.nowMillis() / 1000
			b.lastBytes = info.Bytes
			b.lastName = filepath.Base(path)
		}
		b.mu.Unlock()
		if env.Log == nil {
			return
		}
		if err != nil {
			env.Log.Error("backup failed", "path", path, "err", err)
			return
		}
		env.Log.Info("backup done", "path", path, "tables", info.Tables, "bytes", info.Bytes, "seq", info.Seq, "ms", took.Milliseconds())
	}()
	return resp.Simple("Background saving started")
}

// lastsave is LASTSAVE. It replies with the Unix time of the last checkpoint that
// ended well, or 0.
func lastsave(env *Env, _ *Client, _ [][]byte) resp.Value {
	return resp.Integer(env.Backup.State().LastSave)
}
