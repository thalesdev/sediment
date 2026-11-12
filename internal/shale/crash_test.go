// SPDX-License-Identifier: MIT

//go:build unix

package shale

import (
	"bufio"
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	childDirEnv   = "SHALE_KILL_DIR"
	childStartEnv = "SHALE_KILL_START"
	childSyncEnv  = "SHALE_KILL_SYNC"
	childMemEnv   = "SHALE_KILL_MEM"

	childWritersEnv = "SHALE_KILL_WRITERS"
	childRoundEnv   = "SHALE_KILL_ROUND"
)

func killValue(n int) []byte {
	size := n%40 + 1
	if n%500 == 499 {
		size = 40 << 10
	}
	return append(fmt.Appendf(nil, "value-%08d-", n), bytes.Repeat([]byte{byte('a' + n%26)}, size)...)
}

func parallelKey(round, writer, n int) []byte {
	return fmt.Appendf(nil, "r%02d-w%02d-%08d", round, writer, n)
}

func runParallelChild(dir string, writers int) {
	round, _ := strconv.Atoi(os.Getenv(childRoundEnv))
	memtable, _ := strconv.Atoi(os.Getenv(childMemEnv))
	time.AfterFunc(60*time.Second, func() { os.Exit(3) })
	db, err := Open(dir, Options{Sync: os.Getenv(childSyncEnv) == "1", MemtableSize: memtable})
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: Open:", err)
		os.Exit(2)
	}
	for w := range writers {
		go func() {
			for n := 0; ; n++ {
				if err := db.Put(parallelKey(round, w, n), killValue(n)); err != nil {
					fmt.Fprintln(os.Stderr, "child: Put:", err)
					os.Exit(2)
				}
				if _, err := os.Stdout.WriteString(fmt.Sprintf("%d %d\n", w, n)); err != nil {
					os.Exit(2)
				}
			}
		}()
	}
	select {}
}

func runKillChild(dir string) {
	if writers, _ := strconv.Atoi(os.Getenv(childWritersEnv)); writers > 1 {
		runParallelChild(dir, writers)
	}
	start, _ := strconv.Atoi(os.Getenv(childStartEnv))
	time.AfterFunc(60*time.Second, func() { os.Exit(3) })
	memtable, _ := strconv.Atoi(os.Getenv(childMemEnv))
	db, err := Open(dir, Options{Sync: os.Getenv(childSyncEnv) == "1", MemtableSize: memtable})
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: Open:", err)
		os.Exit(2)
	}
	for n := start; ; n++ {
		if err := db.Put(keyN(n), killValue(n)); err != nil {
			fmt.Fprintln(os.Stderr, "child: Put:", err)
			os.Exit(2)
		}
		if _, err := os.Stdout.WriteString(strconv.Itoa(n) + "\n"); err != nil {
			os.Exit(2)
		}
	}
}

func TestKillNine(t *testing.T) {
	killRounds(t, "TestKillNine", 0)
}

func TestKillNineSmallMemtable(t *testing.T) {
	killRounds(t, "TestKillNineSmallMemtable", 16<<10)
}

func TestKillNineParallel(t *testing.T) {
	killRoundsParallel(t, "TestKillNineParallel", 8, 0)
}

func TestKillNineParallelSmallMemtable(t *testing.T) {
	killRoundsParallel(t, "TestKillNineParallelSmallMemtable", 8, 16<<10)
}

func killRoundsParallel(t *testing.T, name string, writers, memtable int) {
	if dir := os.Getenv(childDirEnv); dir != "" {
		runKillChild(dir)
		os.Exit(0)
	}
	if testing.Short() {
		t.Skip("the test starts and kills processes")
	}

	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(17, 19))
	type confirmed struct{ round, writer, last int }
	var all []confirmed
	for round := range 8 {
		sync := round == 7
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$")
		cmd.Env = append(os.Environ(),
			childDirEnv+"="+dir,
			childWritersEnv+"="+strconv.Itoa(writers),
			childRoundEnv+"="+strconv.Itoa(round),
			childSyncEnv+"="+map[bool]string{false: "0", true: "1"}[sync],
			childMemEnv+"="+strconv.Itoa(memtable),
		)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		last := make([]int, writers)
		for i := range last {
			last[i] = -1
		}
		firstLine := make(chan struct{})
		done := make(chan struct{})
		var bad atomic.Value
		go func() {
			defer close(done)
			r := bufio.NewReader(stdout)
			signalled := false
			for {
				line, err := r.ReadString('\n')
				if !strings.HasSuffix(line, "\n") {
					return
				}
				var w, n int
				if _, scanErr := fmt.Sscanf(line, "%d %d", &w, &n); scanErr == nil && w >= 0 && w < writers {
					if n != last[w]+1 {
						bad.Store(fmt.Sprintf("writer %d confirmed %d after %d", w, n, last[w]))
						return
					}
					last[w] = n
					if !signalled {
						signalled = true
						close(firstLine)
					}
				}
				if err != nil {
					return
				}
			}
		}()
		select {
		case <-firstLine:
		case <-time.After(20 * time.Second):
			cmd.Process.Kill()
			t.Fatalf("round %d: the child did not write in 20 seconds: %s", round, stderr.String())
		}
		time.Sleep(time.Duration(10+rng.IntN(140)) * time.Millisecond)
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		<-done
		cmd.Wait()
		if msg := bad.Load(); msg != nil {
			t.Fatalf("round %d: %v", round, msg)
		}
		for w, n := range last {
			all = append(all, confirmed{round, w, n})
		}

		db := openDB(t, dir, Options{MemtableSize: memtable})
		checkDirectory(t, dir, db)
		checkSeqInvariant(t, db)
		total := 0
		for _, c := range all {
			for n := 0; n <= c.last; n++ {
				got, err := db.Get(parallelKey(c.round, c.writer, n))
				if err != nil || !bytes.Equal(got, killValue(n)) {
					t.Fatalf("round %d: write %d of writer %d in round %d was confirmed but Get = %d bytes, %v", round, n, c.writer, c.round, len(got), err)
				}
			}
			total += c.last + 1
		}
		t.Logf("round %d (sync=%v): %d writes confirmed in all rounds, recovery %+v", round, sync, total, db.Recovery())
		closeDB(t, db)
	}
}

func killRounds(t *testing.T, name string, memtable int) {
	if dir := os.Getenv(childDirEnv); dir != "" {
		runKillChild(dir)
		os.Exit(0)
	}
	if testing.Short() {
		t.Skip("the test starts and kills processes")
	}

	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(7, 11))
	confirmed := -1
	total := 0
	for round := range 10 {
		sync := round == 9
		cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$")
		cmd.Env = append(os.Environ(),
			childDirEnv+"="+dir,
			childStartEnv+"="+strconv.Itoa(confirmed+1),
			childSyncEnv+"="+map[bool]string{false: "0", true: "1"}[sync],
			childMemEnv+"="+strconv.Itoa(memtable),
		)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		lines := make(chan int, 1<<16)
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer close(lines)
			r := bufio.NewReader(stdout)
			for {
				line, err := r.ReadString('\n')
				if !strings.HasSuffix(line, "\n") {
					return
				}
				if n, convErr := strconv.Atoi(strings.TrimSpace(line)); convErr == nil {
					lines <- n
				}
				if err != nil {
					return
				}
			}
		}()

		select {
		case first, ok := <-lines:
			if !ok {
				t.Fatalf("round %d: the child wrote nothing: %s", round, stderr.String())
			}
			if first != confirmed+1 {
				t.Fatalf("round %d: the child started at %d, want %d", round, first, confirmed+1)
			}
			confirmed = first
		case <-time.After(20 * time.Second):
			cmd.Process.Kill()
			t.Fatalf("round %d: the child did not write in 20 seconds: %s", round, stderr.String())
		}
		time.Sleep(time.Duration(10+rng.IntN(140)) * time.Millisecond)
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		<-done
		for n := range lines {
			if n != confirmed+1 {
				t.Fatalf("round %d: the child wrote %d after %d", round, n, confirmed)
			}
			confirmed = n
		}
		cmd.Wait()

		db := openDB(t, dir, Options{MemtableSize: memtable})
		checkDirectory(t, dir, db)
		checkSeqInvariant(t, db)
		for n := 0; n <= confirmed; n++ {
			got, err := db.Get(keyN(n))
			if err != nil || !bytes.Equal(got, killValue(n)) {
				t.Fatalf("round %d: write %d was confirmed but Get = %d bytes, %v", round, n, len(got), err)
			}
		}
		t.Logf("round %d (sync=%v): %d writes confirmed, recovery %+v", round, sync, confirmed+1, db.Recovery())
		scanTables(t, db)
		total = confirmed + 1
		closeDB(t, db)
	}
	if total < 100 {
		t.Fatalf("only %d writes were confirmed in all the rounds", total)
	}
}
