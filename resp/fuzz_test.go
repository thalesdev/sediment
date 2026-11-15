// SPDX-License-Identifier: MIT

package resp

import (
	"bytes"
	"io"
	"runtime"
	"testing"
)

var seeds = []string{
	"*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n", "*1\r\n$4\r\nPING\r\n", "PING\r\n", "SET a \"b\\x41\"\r\n",
	"*-1\r\n", "$-1\r\n", "*1\r\n$99999999999\r\n", "*1000000\r\n", "+OK\r\n", "-ERR x\r\n", ":1\r\n",
	"%1\r\n+a\r\n:1\r\n", "~2\r\n:1\r\n:2\r\n", ">1\r\n+m\r\n", "#t\r\n", ",1.5\r\n", "(12\r\n", "=7\r\ntxt:abc\r\n",
	"!3\r\nerr\r\n", "_\r\n", "*1\r\n*1\r\n*1\r\n:1\r\n", "POST / HTTP/1.1\r\n", "|1\r\n", "$?\r\n",
}

func FuzzReadCommand(f *testing.F) {
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	limits := Limits{MaxBulk: 1 << 20, MaxArgs: 64, MaxInline: 4096, MaxRequest: 1 << 20}
	f.Fuzz(func(t *testing.T, data []byte) {
		r := NewReader(bytes.NewReader(data), limits)
		for range len(data) + 1 {
			args, err := r.ReadCommand()
			if err != nil {
				return
			}
			if len(args) == 0 || len(args) > limits.MaxArgs {
				t.Fatalf("bad number of arguments: %d", len(args))
			}
			for _, a := range args {
				if len(a) > limits.MaxBulk && len(a) > limits.MaxInline {
					t.Fatalf("argument above the limits: %d", len(a))
				}
			}
		}
	})
}

func FuzzReadValue(f *testing.F) {
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	limits := Limits{MaxBulk: 1 << 20, MaxArgs: 64, MaxInline: 4096, MaxNodes: 1024}
	f.Fuzz(func(t *testing.T, data []byte) {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		v, err := NewReader(bytes.NewReader(data), limits).ReadValue()
		runtime.ReadMemStats(&after)
		if used := after.TotalAlloc - before.TotalAlloc; used > 64<<20 {
			t.Fatalf("allocated %d bytes for %d bytes of input", used, len(data))
		}
		if err != nil {
			return
		}
		first := encodeBytes(v)
		again, err := NewReader(bytes.NewReader(first), Limits{MaxBulk: 1 << 20, MaxArgs: 64, MaxInline: 1 << 20, MaxNodes: 1024}).ReadValue()
		if err != nil {
			t.Fatalf("cannot read what the Writer wrote: %v\n%q", err, first)
		}
		if second := encodeBytes(again); !bytes.Equal(first, second) {
			t.Fatalf("round trip changed the bytes:\n%q\n%q", first, second)
		}
	})
}

func encodeBytes(v Value) []byte {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.SetProtocol(3)
	_ = w.WriteValue(v)
	_ = w.Flush()
	return buf.Bytes()
}

type onceReader struct {
	data  []byte
	reads int
}

func (o *onceReader) Read(p []byte) (int, error) {
	o.reads++
	if o.reads > 1 || len(o.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, o.data)
	o.data = o.data[n:]
	return n, nil
}

func FuzzPending(f *testing.F) {
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	limits := Limits{MaxBulk: 1 << 10, MaxArgs: 16, MaxInline: 256, MaxRequest: 4 << 10}
	f.Fuzz(func(t *testing.T, data []byte) {
		src := &onceReader{data: data}
		r := NewReader(src, limits)
		if r.Wait() != nil {
			return
		}
		if !r.Pending() {
			return
		}
		args, err := r.ReadCommand()
		if err != nil && !IsProtocolError(err) {
			t.Fatalf("Pending said a whole command, ReadCommand gave %q, %v", args, err)
		}
		if err == nil && len(args) == 0 {
			t.Fatal("ReadCommand returned no arguments and no error")
		}
		if src.reads > 1 {
			t.Fatal("ReadCommand read from the source after Pending said it was whole")
		}
	})
}
