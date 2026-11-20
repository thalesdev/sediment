// SPDX-License-Identifier: MIT

package resp

import (
	"bufio"
	"bytes"
	"io"
	"math"
	"strconv"
)

// writerBufferSize is the size of the buffer of the Writer.
const writerBufferSize = 16 << 10

// Writer writes RESP values in the version of the protocol that SetProtocol
// chose, RESP2 by default. It is not safe for concurrent use. A write error
// stays, and Flush returns it.
type Writer struct {
	bw    *bufio.Writer
	proto int
	num   []byte
	err   error
}

// NewWriter returns a Writer that writes to w, in RESP2.
func NewWriter(w io.Writer) *Writer {
	return &Writer{bw: bufio.NewWriterSize(w, writerBufferSize), proto: 2}
}

// SetProtocol sets the version of the protocol, 2 or 3. Any other value is
// ignored.
func (w *Writer) SetProtocol(version int) {
	if version == 2 || version == 3 {
		w.proto = version
	}
}

// Protocol returns the version of the protocol, 2 or 3.
func (w *Writer) Protocol() int {
	return w.proto
}

// Buffered returns the number of bytes that wait to be flushed.
func (w *Writer) Buffered() int {
	return w.bw.Buffered()
}

// Flush writes the buffered bytes to the destination. It returns the first error
// of any write since the Writer was created.
func (w *Writer) Flush() error {
	if w.err == nil {
		w.err = w.bw.Flush()
	}
	return w.err
}

// WriteValue writes v. In RESP2 the types that RESP2 does not have are written
// as the closest ones: a null as a null bulk string, a boolean as an integer, a
// double, a big number, a verbatim string and a bulk error as a bulk string or
// an error, and a map, a set and a push as an array.
func (w *Writer) WriteValue(v Value) error {
	w.write(v)
	return w.err
}

// write writes v and keeps the first error.
func (w *Writer) write(v Value) {
	v3 := w.proto == 3
	switch v.Kind {
	case KindSimple:
		w.text('+', v.Bytes)
	case KindError:
		w.text('-', v.Bytes)
	case KindInt:
		w.integer(':', v.Int)
	case KindBulk:
		w.bulk('$', v.Bytes)
	case KindNull:
		if v3 {
			w.raw("_\r\n")
		} else {
			w.raw("$-1\r\n")
		}
	case KindNullArray:
		if v3 {
			w.raw("_\r\n")
		} else {
			w.raw("*-1\r\n")
		}
	case KindBool:
		w.writeBool(v.Bool, v3)
	case KindDouble:
		w.writeDouble(v.Double, v3)
	case KindBigNumber:
		if v3 {
			w.text('(', v.Bytes)
		} else {
			w.bulk('$', v.Bytes)
		}
	case KindVerbatim:
		w.writeVerbatim(v, v3)
	case KindBulkError:
		if v3 {
			w.bulk('!', v.Bytes)
		} else {
			w.text('-', v.Bytes)
		}
	case KindArray:
		w.aggregate('*', len(v.Elems), v.Elems)
	case KindMap:
		if v3 {
			w.aggregate('%', len(v.Elems)/2, v.Elems)
		} else {
			w.aggregate('*', len(v.Elems), v.Elems)
		}
	case KindSet:
		if v3 {
			w.aggregate('~', len(v.Elems), v.Elems)
		} else {
			w.aggregate('*', len(v.Elems), v.Elems)
		}
	case KindPush:
		if v3 {
			w.aggregate('>', len(v.Elems), v.Elems)
		} else {
			w.aggregate('*', len(v.Elems), v.Elems)
		}
	default:
		w.text('-', []byte("ERR internal error"))
	}
}

// writeBool writes a boolean.
func (w *Writer) writeBool(b, v3 bool) {
	switch {
	case v3 && b:
		w.raw("#t\r\n")
	case v3:
		w.raw("#f\r\n")
	case b:
		w.raw(":1\r\n")
	default:
		w.raw(":0\r\n")
	}
}

// writeDouble writes a double.
func (w *Writer) writeDouble(f float64, v3 bool) {
	var text []byte
	switch {
	case math.IsInf(f, 1):
		text = []byte("inf")
	case math.IsInf(f, -1):
		text = []byte("-inf")
	case math.IsNaN(f):
		text = []byte("nan")
	default:
		text = strconv.AppendFloat(nil, f, 'g', -1, 64)
	}
	if v3 {
		w.text(',', text)
	} else {
		w.bulk('$', text)
	}
}

// writeVerbatim writes a verbatim string.
func (w *Writer) writeVerbatim(v Value, v3 bool) {
	if !v3 {
		w.bulk('$', v.Bytes)
		return
	}
	format := v.Format
	for len(format) < 3 {
		format += " "
	}
	body := make([]byte, 0, 4+len(v.Bytes))
	body = append(body, format[:3]...)
	body = append(body, ':')
	body = append(body, v.Bytes...)
	w.bulk('=', body)
}

// aggregate writes the header of an aggregate with n entries, and then elems.
func (w *Writer) aggregate(prefix byte, n int, elems []Value) {
	w.integer(prefix, int64(n))
	for _, e := range elems {
		w.write(e)
	}
}

// text writes a line of text. A CR or a LF in the text becomes a space, because
// they would end the line.
func (w *Writer) text(prefix byte, s []byte) {
	w.byte(prefix)
	if bytes.ContainsAny(s, "\r\n") {
		clean := bytes.Clone(s)
		for i, c := range clean {
			if c == '\r' || c == '\n' {
				clean[i] = ' '
			}
		}
		s = clean
	}
	w.bytes(s)
	w.raw("\r\n")
}

// integer writes a line that holds a number.
func (w *Writer) integer(prefix byte, n int64) {
	w.byte(prefix)
	w.num = strconv.AppendInt(w.num[:0], n, 10)
	w.bytes(w.num)
	w.raw("\r\n")
}

// bulk writes a length and a body.
func (w *Writer) bulk(prefix byte, b []byte) {
	w.integer(prefix, int64(len(b)))
	w.bytes(b)
	w.raw("\r\n")
}

// byte writes one byte.
func (w *Writer) byte(c byte) {
	if w.err == nil {
		w.err = w.bw.WriteByte(c)
	}
}

// bytes writes b.
func (w *Writer) bytes(b []byte) {
	if w.err == nil {
		_, w.err = w.bw.Write(b)
	}
}

// raw writes s.
func (w *Writer) raw(s string) {
	if w.err == nil {
		_, w.err = w.bw.WriteString(s)
	}
}
