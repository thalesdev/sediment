// SPDX-License-Identifier: MIT

package resp

import (
	"errors"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func cmd(t *testing.T, input string, l Limits) ([][]byte, error) {
	t.Helper()
	return NewReader(strings.NewReader(input), l).ReadCommand()
}

func strs(args [][]byte) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = string(a)
	}
	return out
}

func TestReadCommand(t *testing.T) {
	t.Run("Should read an array of bulk strings", func(t *testing.T) {
		args, err := cmd(t, "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$2\r\nv1\r\n", Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if got := strs(args); !reflect.DeepEqual(got, []string{"SET", "k", "v1"}) {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("Should read a binary safe bulk string", func(t *testing.T) {
		args, err := cmd(t, "*1\r\n$4\r\na\r\nb\r\n", Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if string(args[0]) != "a\r\nb" {
			t.Fatalf("got %q", args[0])
		}
	})

	t.Run("Should read an empty bulk string", func(t *testing.T) {
		args, err := cmd(t, "*2\r\n$3\r\nGET\r\n$0\r\n\r\n", Limits{})
		if err != nil || len(args) != 2 || len(args[1]) != 0 {
			t.Fatalf("got %q, %v", args, err)
		}
	})

	t.Run("Should read inline commands", func(t *testing.T) {
		args, err := cmd(t, "SET  key \"a b\\x41\\n\" 'it\\'s'\r\n", Limits{})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"SET", "key", "a bA\n", "it's"}
		if got := strs(args); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("Should accept a bare line feed in an inline command", func(t *testing.T) {
		args, err := cmd(t, "PING\n", Limits{})
		if err != nil || len(args) != 1 {
			t.Fatalf("got %q, %v", args, err)
		}
	})

	t.Run("Should skip empty lines and empty arrays", func(t *testing.T) {
		args, err := cmd(t, "\r\n*0\r\n*-1\r\n   \r\nPING\r\n", Limits{})
		if err != nil || len(args) != 1 || string(args[0]) != "PING" {
			t.Fatalf("got %q, %v", args, err)
		}
	})

	t.Run("Should read pipelined commands one by one", func(t *testing.T) {
		r := NewReader(strings.NewReader("*1\r\n$4\r\nPING\r\n*2\r\n$4\r\nECHO\r\n$1\r\nx\r\n"), Limits{})
		for _, want := range []int{1, 2} {
			args, err := r.ReadCommand()
			if err != nil || len(args) != want {
				t.Fatalf("got %q, %v", args, err)
			}
		}
		if _, err := r.ReadCommand(); err != io.EOF {
			t.Fatalf("want io.EOF, got %v", err)
		}
	})

	t.Run("Should return io.EOF only at the start of a command", func(t *testing.T) {
		if _, err := cmd(t, "", Limits{}); err != io.EOF {
			t.Fatalf("got %v", err)
		}
		for _, in := range []string{"*2\r\n$3\r\nGET\r\n", "*1\r\n$3\r\nab", "*1\r\n$3", "*1\r\n$3\r\nabc\r", "*2"} {
			if _, err := cmd(t, in, Limits{}); err != io.ErrUnexpectedEOF {
				t.Fatalf("%q: got %v", in, err)
			}
		}
	})

	t.Run("Should refuse bad lengths", func(t *testing.T) {
		for _, in := range []string{
			"*x\r\n", "*-2\r\n", "*99999999999999999999\r\n", "*1025\r\n", "*2147483648\r\n",
			"*1\r\n$-1\r\n", "*1\r\n$-2\r\n", "*1\r\n$x\r\n", "*1\r\n$\r\n", "*1\r\n$17000000\r\n",
			"*1\r\n:1\r\n", "*1\r\n$3\r\nabcde\r\n", "*1\r\n$3\r\nabc\n\n", "*1\r\n$+3\r\nabc\r\n",
		} {
			_, err := cmd(t, in, Limits{})
			if !IsProtocolError(err) {
				t.Fatalf("%q: want a protocol error, got %v", in, err)
			}
		}
	})

	t.Run("Should refuse a request above the total limit", func(t *testing.T) {
		l := Limits{MaxBulk: 8, MaxRequest: 10}
		_, err := cmd(t, "*2\r\n$8\r\naaaaaaaa\r\n$8\r\nbbbbbbbb\r\n", l)
		if !IsProtocolError(err) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("Should refuse a too big inline line", func(t *testing.T) {
		_, err := cmd(t, strings.Repeat("a", 200)+"\r\n", Limits{MaxInline: 100})
		var pe *ProtocolError
		if !errors.As(err, &pe) || pe.Msg != "too big inline request" {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("Should refuse too many inline words", func(t *testing.T) {
		_, err := cmd(t, strings.Repeat("a ", 20)+"\r\n", Limits{MaxArgs: 10})
		if !IsProtocolError(err) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("Should refuse unbalanced quotes", func(t *testing.T) {
		for _, in := range []string{"SET \"abc\r\n", "SET 'abc\r\n", "SET \"a\"b\r\n", "SET 'a'b\r\n"} {
			if _, err := cmd(t, in, Limits{}); !IsProtocolError(err) {
				t.Fatalf("%q: got %v", in, err)
			}
		}
	})

	t.Run("Should close a connection that sends an HTTP request", func(t *testing.T) {
		for _, in := range []string{"POST / HTTP/1.1\r\n", "post /x\r\n", "Host: example.com\r\n"} {
			if _, err := cmd(t, in, Limits{}); err != ErrCrossProtocol {
				t.Fatalf("%q: got %v", in, err)
			}
		}
	})

	t.Run("Should allocate by the bytes that arrive", func(t *testing.T) {
		for _, in := range []string{"*1\r\n$1000000000\r\nabc", "*1\r\n$16777216\r\nabc"} {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			_, err := cmd(t, in, Limits{MaxBulk: 1 << 30, MaxRequest: 1 << 30})
			runtime.ReadMemStats(&after)
			if err != io.ErrUnexpectedEOF {
				t.Fatalf("got %v", err)
			}
			if used := after.TotalAlloc - before.TotalAlloc; used > 1<<20 {
				t.Fatalf("%q: allocated %d bytes", in, used)
			}
		}
	})

	t.Run("Should not allocate for the declared number of arguments", func(t *testing.T) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err := cmd(t, "*1000000\r\n", Limits{MaxArgs: 1 << 30})
		runtime.ReadMemStats(&after)
		if err != io.ErrUnexpectedEOF {
			t.Fatalf("got %v", err)
		}
		if used := after.TotalAlloc - before.TotalAlloc; used > 1<<20 {
			t.Fatalf("allocated %d bytes", used)
		}
	})

	t.Run("Should read a large bulk string in steps", func(t *testing.T) {
		body := strings.Repeat("x", 300000)
		args, err := cmd(t, "*1\r\n$300000\r\n"+body+"\r\n", Limits{})
		if err != nil || string(args[0]) != body {
			t.Fatalf("err %v, len %d", err, len(args[0]))
		}
	})
}

func TestReadValue(t *testing.T) {
	read := func(t *testing.T, in string, l Limits) (Value, error) {
		t.Helper()
		return NewReader(strings.NewReader(in), l).ReadValue()
	}

	t.Run("Should read every RESP2 and RESP3 type", func(t *testing.T) {
		cases := map[string]Value{
			"+OK\r\n":                     Simple("OK"),
			"-ERR bad\r\n":                Error("ERR bad"),
			":-42\r\n":                    Integer(-42),
			"$5\r\nhello\r\n":             BulkString("hello"),
			"$-1\r\n":                     Null(),
			"*-1\r\n":                     Null(),
			"_\r\n":                       Null(),
			"#t\r\n":                      {Kind: KindBool, Bool: true},
			"#f\r\n":                      {Kind: KindBool},
			",1.5\r\n":                    {Kind: KindDouble, Double: 1.5},
			"(-123456789012345678901\r\n": {Kind: KindBigNumber, Bytes: []byte("-123456789012345678901")},
			"=7\r\ntxt:abc\r\n":           {Kind: KindVerbatim, Format: "txt", Bytes: []byte("abc")},
			"!3\r\nerr\r\n":               {Kind: KindBulkError, Bytes: []byte("err")},
			"*2\r\n:1\r\n$1\r\na\r\n":     Array(Integer(1), BulkString("a")),
			"~1\r\n:1\r\n":                {Kind: KindSet, Elems: []Value{Integer(1)}},
			">1\r\n+x\r\n":                {Kind: KindPush, Elems: []Value{Simple("x")}},
			"%1\r\n+k\r\n:1\r\n":          Map(Simple("k"), Integer(1)),
		}
		for in, want := range cases {
			got, err := read(t, in, Limits{})
			if err != nil {
				t.Fatalf("%q: %v", in, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%q: got %+v, want %+v", in, got, want)
			}
		}
	})

	t.Run("Should read infinity and not a number", func(t *testing.T) {
		v, err := read(t, ",inf\r\n", Limits{})
		if err != nil || v.Double <= 0 {
			t.Fatalf("got %+v, %v", v, err)
		}
		v, err = read(t, ",nan\r\n", Limits{})
		if err != nil || v.Double == v.Double {
			t.Fatalf("got %+v, %v", v, err)
		}
	})

	t.Run("Should refuse malformed values", func(t *testing.T) {
		for _, in := range []string{
			"?\r\n", "#x\r\n", "_x\r\n", ",abc\r\n", ",0x10\r\n", "(\r\n", "(1a\r\n", "=3\r\nabc\r\n",
			"=7\r\ntxtxabc\r\n", "|1\r\n+a\r\n+b\r\n+c\r\n", "$?\r\n", "*?\r\n", "$-5\r\n", ":9223372036854775808\r\n",
			"+ab\ncd\r\n",
		} {
			if _, err := read(t, in, Limits{}); !IsProtocolError(err) {
				t.Fatalf("%q: got %v", in, err)
			}
		}
	})

	t.Run("Should refuse nesting that is too deep", func(t *testing.T) {
		in := strings.Repeat("*1\r\n", 33) + ":1\r\n"
		if _, err := read(t, in, Limits{}); !IsProtocolError(err) {
			t.Fatalf("got %v", err)
		}
		in = strings.Repeat("*1\r\n", 32) + ":1\r\n"
		if _, err := read(t, in, Limits{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Should refuse a value with too many nodes", func(t *testing.T) {
		in := "*3\r\n*3\r\n:1\r\n:1\r\n:1\r\n*3\r\n:1\r\n:1\r\n:1\r\n*3\r\n:1\r\n:1\r\n:1\r\n"
		if _, err := read(t, in, Limits{MaxNodes: 8}); !IsProtocolError(err) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("Should return io.EOF at the start and an unexpected EOF inside", func(t *testing.T) {
		if _, err := read(t, "", Limits{}); err != io.EOF {
			t.Fatalf("got %v", err)
		}
		for _, in := range []string{"*2\r\n:1\r\n", "+OK", "$3\r\nab", "%1\r\n+k\r\n"} {
			if _, err := read(t, in, Limits{}); err != io.ErrUnexpectedEOF {
				t.Fatalf("%q: got %v", in, err)
			}
		}
	})

	t.Run("Should read a long simple string over the buffer", func(t *testing.T) {
		body := strings.Repeat("a", 40000)
		v, err := read(t, "+"+body+"\r\n", Limits{})
		if err != nil || string(v.Bytes) != body {
			t.Fatalf("err %v", err)
		}
		if _, err := read(t, "+"+body+"\r\n", Limits{MaxInline: 1000}); !IsProtocolError(err) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestParseInt(t *testing.T) {
	t.Run("Should parse the whole range and refuse overflow", func(t *testing.T) {
		ok := map[string]int64{"0": 0, "-0": 0, "9223372036854775807": 9223372036854775807, "-9223372036854775808": -9223372036854775808}
		for in, want := range ok {
			if got, valid := parseInt([]byte(in)); !valid || got != want {
				t.Fatalf("%q: got %d, %v", in, got, valid)
			}
		}
		for _, in := range []string{"", "-", "+1", "1 ", "9223372036854775808", "-9223372036854775809", "99999999999999999999999", "1e3", "0x1"} {
			if _, valid := parseInt([]byte(in)); valid {
				t.Fatalf("%q must fail", in)
			}
		}
	})
}

func TestPending(t *testing.T) {
	read := func(input string) *Reader {
		r := NewReader(strings.NewReader(input), Limits{MaxBulk: 64, MaxArgs: 4, MaxInline: 32, MaxRequest: 100})
		if err := r.Wait(); err != nil {
			t.Fatal(err)
		}
		return r
	}

	t.Run("Should see a whole command in the buffer", func(t *testing.T) {
		for _, in := range []string{
			"*1\r\n$4\r\nPING\r\n", "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n", "*2\r\n$3\r\nGET\r\n$0\r\n\r\n", "PING\r\n", "PING\n",
		} {
			if !read(in).Pending() {
				t.Fatalf("%q is whole", in)
			}
		}
	})

	t.Run("Should not see a partial command", func(t *testing.T) {
		for _, in := range []string{
			"*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\n", "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv", "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r",
			"*2", "*2\r\n", "*1\r\n$", "*1\r\n$4\r\nPI", "PING",
		} {
			if read(in).Pending() {
				t.Fatalf("%q is partial", in)
			}
		}
	})

	t.Run("Should leave a bad or empty command to ReadCommand", func(t *testing.T) {
		for _, in := range []string{
			"\r\n", "\n", "  \t\r\n", "*0\r\n", "*-1\r\n", "*x\r\n", "*5\r\n", "*1\r\n:1\r\n", "*1\r\n$-1\r\n", "*1\r\n$65\r\n" + strings.Repeat("a", 65) + "\r\n",
			"*1\r\n$3\r\nabcde\r\n", strings.Repeat("a", 40) + "\r\n", "*2\r\n$60\r\n" + strings.Repeat("a", 60) + "\r\n$60\r\n" + strings.Repeat("a", 60) + "\r\n",
		} {
			if read(in).Pending() {
				t.Fatalf("%q must be left to ReadCommand", in)
			}
		}
	})

	t.Run("Should not consume the bytes", func(t *testing.T) {
		r := read("*1\r\n$4\r\nPING\r\n*1\r\n$4\r\nECHO\r\n")
		if !r.Pending() || !r.Pending() {
			t.Fatal("not pending")
		}
		args, err := r.ReadCommand()
		if err != nil || string(args[0]) != "PING" {
			t.Fatalf("got %q, %v", args, err)
		}
		if !r.Pending() {
			t.Fatal("the second command is whole")
		}
		args, _ = r.ReadCommand()
		if string(args[0]) != "ECHO" {
			t.Fatalf("got %q", args)
		}
		if r.Pending() {
			t.Fatal("nothing is left")
		}
	})
}
