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
	"testing"
	"time"
)

const (
	childDirEnv   = "SHALE_KILL_DIR"
	childStartEnv = "SHALE_KILL_START"
	childSyncEnv  = "SHALE_KILL_SYNC"
	childMemEnv   = "SHALE_KILL_MEM"
)

func killValue(n int) []byte {
	size := n%40 + 1
	if n%500 == 499 {
		size = 40 << 10
	}
	return append(fmt.Appendf(nil, "value-%08d-", n), bytes.Repeat([]byte{byte('a' + n%26)}, size)...)
}

func runKillChild(dir string) {
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
