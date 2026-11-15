// SPDX-License-Identifier: MIT

package resp

import (
	"bytes"
	"math"
	"reflect"
	"testing"
)

func encode(t *testing.T, version int, v Value) string {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.SetProtocol(version)
	if err := w.WriteValue(v); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestWriter(t *testing.T) {
	t.Run("Should write the RESP2 types", func(t *testing.T) {
		cases := []struct {
			v    Value
			want string
		}{
			{Simple("OK"), "+OK\r\n"},
			{Error("ERR x"), "-ERR x\r\n"},
			{Integer(-7), ":-7\r\n"},
			{BulkString("hi"), "$2\r\nhi\r\n"},
			{BulkString(""), "$0\r\n\r\n"},
			{Null(), "$-1\r\n"},
			{Array(Integer(1), Null()), "*2\r\n:1\r\n$-1\r\n"},
			{Array(), "*0\r\n"},
		}
		for _, c := range cases {
			if got := encode(t, 2, c.v); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		}
	})

	t.Run("Should write the RESP3 types", func(t *testing.T) {
		cases := []struct {
			v    Value
			want string
		}{
			{Null(), "_\r\n"},
			{Value{Kind: KindBool, Bool: true}, "#t\r\n"},
			{Value{Kind: KindBool}, "#f\r\n"},
			{Value{Kind: KindDouble, Double: 1.5}, ",1.5\r\n"},
			{Value{Kind: KindDouble, Double: math.Inf(-1)}, ",-inf\r\n"},
			{Value{Kind: KindBigNumber, Bytes: []byte("123")}, "(123\r\n"},
			{Value{Kind: KindVerbatim, Format: "txt", Bytes: []byte("abc")}, "=7\r\ntxt:abc\r\n"},
			{Value{Kind: KindBulkError, Bytes: []byte("e")}, "!1\r\ne\r\n"},
			{Map(Simple("a"), Integer(1)), "%1\r\n+a\r\n:1\r\n"},
			{Value{Kind: KindSet, Elems: []Value{Integer(1)}}, "~1\r\n:1\r\n"},
			{Value{Kind: KindPush, Elems: []Value{Simple("m")}}, ">1\r\n+m\r\n"},
		}
		for _, c := range cases {
			if got := encode(t, 3, c.v); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		}
	})

	t.Run("Should write the RESP3 types as the closest RESP2 ones", func(t *testing.T) {
		cases := []struct {
			v    Value
			want string
		}{
			{Value{Kind: KindBool, Bool: true}, ":1\r\n"},
			{Value{Kind: KindDouble, Double: 1.5}, "$3\r\n1.5\r\n"},
			{Value{Kind: KindBigNumber, Bytes: []byte("123")}, "$3\r\n123\r\n"},
			{Value{Kind: KindVerbatim, Format: "txt", Bytes: []byte("abc")}, "$3\r\nabc\r\n"},
			{Value{Kind: KindBulkError, Bytes: []byte("e")}, "-e\r\n"},
			{Map(Simple("a"), Integer(1)), "*2\r\n+a\r\n:1\r\n"},
			{Value{Kind: KindSet, Elems: []Value{Integer(1)}}, "*1\r\n:1\r\n"},
		}
		for _, c := range cases {
			if got := encode(t, 2, c.v); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		}
	})

	t.Run("Should not let a line break end a line early", func(t *testing.T) {
		got := encode(t, 2, Error("ERR a\r\n+OK"))
		if got != "-ERR a  +OK\r\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("Should ignore an unknown protocol version", func(t *testing.T) {
		w := NewWriter(&bytes.Buffer{})
		w.SetProtocol(9)
		if w.Protocol() != 2 {
			t.Fatalf("got %d", w.Protocol())
		}
		w.SetProtocol(3)
		if w.Protocol() != 3 {
			t.Fatalf("got %d", w.Protocol())
		}
	})

	t.Run("Should round trip the values in RESP3", func(t *testing.T) {
		values := []Value{
			Simple("OK"), Error("ERR x"), Integer(math.MinInt64), BulkString("a\r\nb"), BulkString(""),
			Null(), {Kind: KindBool, Bool: true}, {Kind: KindDouble, Double: 0.1},
			{Kind: KindBigNumber, Bytes: []byte("-99999999999999999999999")},
			{Kind: KindVerbatim, Format: "mkd", Bytes: []byte("# t")},
			{Kind: KindBulkError, Bytes: []byte("SYNTAX bad")},
			Array(Map(Simple("k"), Array(Integer(1), Integer(2))), Value{Kind: KindSet, Elems: []Value{Null()}}),
			{Kind: KindPush, Elems: []Value{Simple("pubsub")}},
		}
		for _, v := range values {
			wire := encode(t, 3, v)
			got, err := NewReader(bytes.NewReader([]byte(wire)), Limits{}).ReadValue()
			if err != nil {
				t.Fatalf("%q: %v", wire, err)
			}
			if !reflect.DeepEqual(got, normalize(v)) {
				t.Fatalf("%q: got %+v, want %+v", wire, got, v)
			}
		}
	})
}

func normalize(v Value) Value {
	if v.Kind == KindBulk && v.Bytes == nil {
		v.Bytes = []byte{}
	}
	if len(v.Elems) > 0 {
		elems := make([]Value, len(v.Elems))
		for i, e := range v.Elems {
			elems[i] = normalize(e)
		}
		v.Elems = elems
	}
	return v
}
