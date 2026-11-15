// SPDX-License-Identifier: MIT

package resp

import (
	"bufio"
	"bytes"
	"io"
	"math"
	"slices"
	"strconv"
)

const (
	// readerBufferSize is the size of the buffer of the Reader.
	readerBufferSize = 16 << 10

	// firstChunk is the size of the first allocation for a bulk string that is
	// larger than it. The next allocations double the size of the data that has
	// arrived.
	firstChunk = 64 << 10

	// maxNumberLine is the largest size of the line of a length or a number.
	maxNumberLine = 64
)

// Reader reads RESP values and commands. It is not safe for concurrent use.
type Reader struct {
	br     *bufio.Reader
	limits Limits
	line   []byte
	nodes  int
}

// NewReader returns a Reader that reads from r with the limits l. A field of l
// that is not set takes its default.
func NewReader(r io.Reader, l Limits) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, readerBufferSize), limits: l.WithDefaults()}
}

// SetLimits changes the limits of the Reader. A field that is not set takes its
// default. The server uses it to apply tighter limits until a client signs in.
func (r *Reader) SetLimits(l Limits) {
	r.limits = l.WithDefaults()
}

// Buffered returns the number of bytes that the Reader has read from its source
// and that ReadCommand or ReadValue did not use yet. When it is 0, a server that
// answers pipelined commands can flush its output.
func (r *Reader) Buffered() int {
	return r.br.Buffered()
}

// Wait blocks until at least one byte is ready to read, or until the input ends
// or fails. It reads nothing from the caller's point of view. A server calls it
// to wait for the first byte of a command with an idle timeout, and then reads
// the command with a shorter one.
func (r *Reader) Wait() error {
	_, err := r.br.Peek(1)
	return err
}

// ReadCommand reads one command of a client. A command is an array of bulk
// strings, or an inline command, which is a line of words. It skips empty lines
// and empty arrays. It returns io.EOF only if the input ends before the first
// byte of a command, and io.ErrUnexpectedEOF if it ends inside one. The slices
// that it returns are new, and the caller can keep them.
func (r *Reader) ReadCommand() ([][]byte, error) {
	for {
		head, err := r.br.Peek(1)
		if err != nil {
			return nil, err
		}
		if head[0] != '*' {
			args, err := r.readInline()
			if err != nil {
				return nil, err
			}
			if len(args) == 0 {
				continue
			}
			return args, nil
		}
		_, _ = r.br.Discard(1)
		n, err := r.readLength("invalid multibulk length")
		if err != nil {
			return nil, err
		}
		if n <= 0 {
			continue
		}
		if n > r.limits.MaxArgs {
			return nil, protocolError("invalid multibulk length")
		}
		return r.readBulkArray(n)
	}
}

// readBulkArray reads n bulk strings, after the header of the array.
func (r *Reader) readBulkArray(n int) ([][]byte, error) {
	args := make([][]byte, 0, min(n, 16))
	total := 0
	for range n {
		c, err := r.br.ReadByte()
		if err != nil {
			return nil, unexpected(err)
		}
		if c != '$' {
			return nil, protocolError("expected '$', got '" + printable(c) + "'")
		}
		size, err := r.readLength("invalid bulk length")
		if err != nil {
			return nil, err
		}
		if size < 0 || size > r.limits.MaxBulk {
			return nil, protocolError("invalid bulk length")
		}
		total += size
		if total > r.limits.MaxRequest {
			return nil, protocolError("request too big")
		}
		data, err := r.readBulkBody(size)
		if err != nil {
			return nil, err
		}
		args = append(args, data)
	}
	return args, nil
}

// ReadValue reads one RESP2 or RESP3 value. It returns io.EOF only if the input
// ends before the first byte of the value.
func (r *Reader) ReadValue() (Value, error) {
	r.nodes = 0
	c, err := r.br.ReadByte()
	if err != nil {
		return Value{}, err
	}
	return r.readValue(c, 0)
}

// readValue reads the value whose type byte is c. depth is the number of
// aggregates that contain it.
func (r *Reader) readValue(c byte, depth int) (Value, error) {
	r.nodes++
	if r.nodes > r.limits.MaxNodes {
		return Value{}, protocolError("too many values")
	}
	switch c {
	case '+', '-':
		line, err := r.readLine(r.limits.MaxInline, false)
		if err != nil {
			return Value{}, err
		}
		kind := KindSimple
		if c == '-' {
			kind = KindError
		}
		return Value{Kind: kind, Bytes: bytes.Clone(line)}, nil
	case ':':
		n, err := r.readInteger("invalid integer")
		return Value{Kind: KindInt, Int: n}, err
	case '$', '!', '=':
		return r.readBulkValue(c)
	case '_':
		line, err := r.readLine(maxNumberLine, false)
		if err != nil {
			return Value{}, err
		}
		if len(line) != 0 {
			return Value{}, protocolError("invalid null")
		}
		return Null(), nil
	case '#':
		line, err := r.readLine(maxNumberLine, false)
		if err != nil {
			return Value{}, err
		}
		if len(line) != 1 || (line[0] != 't' && line[0] != 'f') {
			return Value{}, protocolError("invalid boolean")
		}
		return Value{Kind: KindBool, Bool: line[0] == 't'}, nil
	case ',':
		return r.readDouble()
	case '(':
		return r.readBigNumber()
	case '*', '~', '>', '%':
		return r.readAggregate(c, depth)
	case '|':
		return Value{}, protocolError("attributes are not supported")
	}
	return Value{}, protocolError("unknown type '" + printable(c) + "'")
}

// readBulkValue reads a bulk string, a bulk error or a verbatim string, after
// its type byte c.
func (r *Reader) readBulkValue(c byte) (Value, error) {
	size, err := r.readLength("invalid bulk length")
	if err != nil {
		return Value{}, err
	}
	if size == -1 && c == '$' {
		return Null(), nil
	}
	if size < 0 || size > r.limits.MaxBulk {
		return Value{}, protocolError("invalid bulk length")
	}
	data, err := r.readBulkBody(size)
	if err != nil {
		return Value{}, err
	}
	switch c {
	case '!':
		return Value{Kind: KindBulkError, Bytes: data}, nil
	case '=':
		if len(data) < 4 || data[3] != ':' {
			return Value{}, protocolError("invalid verbatim string")
		}
		return Value{Kind: KindVerbatim, Format: string(data[:3]), Bytes: data[4:]}, nil
	}
	return Value{Kind: KindBulk, Bytes: data}, nil
}

// readDouble reads a double, after its type byte.
func (r *Reader) readDouble() (Value, error) {
	line, err := r.readLine(maxNumberLine, false)
	if err != nil {
		return Value{}, err
	}
	f, ok := parseDouble(line)
	if !ok {
		return Value{}, protocolError("invalid double")
	}
	return Value{Kind: KindDouble, Double: f}, nil
}

// readBigNumber reads a big number, after its type byte.
func (r *Reader) readBigNumber() (Value, error) {
	line, err := r.readLine(r.limits.MaxInline, false)
	if err != nil {
		return Value{}, err
	}
	digits := line
	if len(digits) > 0 && (digits[0] == '-' || digits[0] == '+') {
		digits = digits[1:]
	}
	if len(digits) == 0 {
		return Value{}, protocolError("invalid big number")
	}
	for _, d := range digits {
		if d < '0' || d > '9' {
			return Value{}, protocolError("invalid big number")
		}
	}
	return Value{Kind: KindBigNumber, Bytes: bytes.Clone(line)}, nil
}

// readAggregate reads an array, a set, a push or a map, after its type byte c.
func (r *Reader) readAggregate(c byte, depth int) (Value, error) {
	if depth >= r.limits.MaxDepth {
		return Value{}, protocolError("nesting too deep")
	}
	n, err := r.readLength("invalid aggregate length")
	if err != nil {
		return Value{}, err
	}
	if n == -1 && c == '*' {
		return Null(), nil
	}
	if n < 0 || n > r.limits.MaxArgs {
		return Value{}, protocolError("invalid aggregate length")
	}
	count := n
	kind := KindArray
	switch c {
	case '~':
		kind = KindSet
	case '>':
		kind = KindPush
	case '%':
		kind = KindMap
		count = n * 2
	}
	if r.nodes+count > r.limits.MaxNodes {
		return Value{}, protocolError("too many values")
	}
	elems := make([]Value, 0, min(count, 16))
	for range count {
		t, err := r.br.ReadByte()
		if err != nil {
			return Value{}, unexpected(err)
		}
		v, err := r.readValue(t, depth+1)
		if err != nil {
			return Value{}, unexpected(err)
		}
		elems = append(elems, v)
	}
	return Value{Kind: kind, Elems: elems}, nil
}

// readInteger reads a line that holds one integer.
func (r *Reader) readInteger(msg string) (int64, error) {
	line, err := r.readLine(maxNumberLine, false)
	if err != nil {
		return 0, err
	}
	n, ok := parseInt(line)
	if !ok {
		return 0, protocolError(msg)
	}
	return n, nil
}

// readLength reads a line that holds a length. The length is at least -1 and at
// most math.MaxInt32, so it fits an int on every platform.
func (r *Reader) readLength(msg string) (int, error) {
	n, err := r.readInteger(msg)
	if err != nil {
		return 0, err
	}
	if n < -1 || n > math.MaxInt32 {
		return 0, protocolError(msg)
	}
	return int(n), nil
}

// readLine reads a line up to its end of line and returns it without the end of
// line. If lenient is false the end of line must be CR LF. If it is true, a bare
// LF also ends the line. The slice is valid until the next read. A line that is
// longer than max bytes is a protocol error.
func (r *Reader) readLine(max int, lenient bool) ([]byte, error) {
	line, err := r.br.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		r.line = append(r.line[:0], line...)
		for err == bufio.ErrBufferFull {
			if len(r.line) > max+2 {
				return nil, protocolError("line too long")
			}
			line, err = r.br.ReadSlice('\n')
			r.line = append(r.line, line...)
		}
		line = r.line
	}
	if err != nil {
		return nil, unexpected(err)
	}
	if len(line) > max+2 {
		return nil, protocolError("line too long")
	}
	line = line[:len(line)-1]
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1], nil
	}
	if !lenient {
		return nil, protocolError("invalid end of line")
	}
	return line, nil
}

// readBulkBody reads size bytes and the CR LF after them. It allocates in steps,
// so the memory that it uses follows the bytes that arrive and not the size that
// the client declared.
func (r *Reader) readBulkBody(size int) ([]byte, error) {
	var data []byte
	if size <= firstChunk {
		data = make([]byte, size)
		if _, err := io.ReadFull(r.br, data); err != nil {
			return nil, unexpected(err)
		}
	} else {
		data = make([]byte, 0, firstChunk)
		for len(data) < size {
			step := min(size-len(data), max(len(data), firstChunk))
			old := len(data)
			data = slices.Grow(data, step)[:old+step]
			if _, err := io.ReadFull(r.br, data[old:]); err != nil {
				return nil, unexpected(err)
			}
		}
	}
	var end [2]byte
	if _, err := io.ReadFull(r.br, end[:]); err != nil {
		return nil, unexpected(err)
	}
	if end != [2]byte{'\r', '\n'} {
		return nil, protocolError("invalid end of bulk string")
	}
	return data, nil
}

// unexpected turns io.EOF into io.ErrUnexpectedEOF. It is for the reads that
// happen inside a value.
func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// printable returns c as text, or "?" if it is not a printable ASCII byte.
func printable(c byte) string {
	if c < 0x20 || c > 0x7e {
		return "?"
	}
	return string(rune(c))
}

// parseInt parses a decimal integer of an optional minus sign and digits. It
// fails for an empty text, for any other byte and for a number that does not fit
// an int64.
func parseInt(b []byte) (int64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	neg := b[0] == '-'
	if neg {
		b = b[1:]
		if len(b) == 0 {
			return 0, false
		}
	}
	var n uint64
	for _, d := range b {
		if d < '0' || d > '9' {
			return 0, false
		}
		digit := uint64(d - '0')
		if n > (math.MaxUint64-digit)/10 {
			return 0, false
		}
		n = n*10 + digit
	}
	if neg {
		if n > 1<<63 {
			return 0, false
		}
		return -int64(n), true
	}
	if n > math.MaxInt64 {
		return 0, false
	}
	return int64(n), true
}

// parseDouble parses the text of a RESP3 double: a decimal number, "inf", "-inf"
// or "nan".
func parseDouble(b []byte) (float64, bool) {
	switch string(b) {
	case "inf", "+inf":
		return math.Inf(1), true
	case "-inf":
		return math.Inf(-1), true
	case "nan":
		return math.NaN(), true
	}
	for _, c := range b {
		if (c < '0' || c > '9') && c != '.' && c != '-' && c != '+' && c != 'e' && c != 'E' {
			return 0, false
		}
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// Pending reports whether the bytes that the Reader has already read hold one
// whole command, so that ReadCommand returns without waiting for the network. It
// does not consume anything. It returns false if the command is partial, if it is
// an empty line or an empty array, which ReadCommand skips and then waits for the
// next command, if it is malformed, or if it is larger than what the buffer holds, so a caller
// that batches commands stops at those and lets ReadCommand deal with them.
func (r *Reader) Pending() bool {
	n := r.br.Buffered()
	if n == 0 {
		return false
	}
	buf, err := r.br.Peek(n)
	if err != nil {
		return false
	}
	if buf[0] != '*' {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 || i > r.limits.MaxInline {
			return false
		}
		return len(bytes.TrimFunc(buf[:i], func(c rune) bool { return c < 0x80 && isSpace(byte(c)) })) > 0
	}
	pos := 1
	count, ok := scanNumber(buf, &pos)
	if !ok || count <= 0 || count > r.limits.MaxArgs {
		return false
	}
	total := 0
	for range count {
		if pos >= len(buf) || buf[pos] != '$' {
			return false
		}
		pos++
		size, ok := scanNumber(buf, &pos)
		if !ok || size < 0 || size > r.limits.MaxBulk {
			return false
		}
		total += size
		if total > r.limits.MaxRequest || pos+size+2 > len(buf) {
			return false
		}
		if buf[pos+size] != '\r' || buf[pos+size+1] != '\n' {
			return false
		}
		pos += size + 2
	}
	return true
}

// scanNumber reads a number and its CR LF at buf[*pos:], and moves *pos after
// them. It fails if the line is not whole or if it is not a number.
func scanNumber(buf []byte, pos *int) (int, bool) {
	i := bytes.Index(buf[*pos:], []byte("\r\n"))
	if i < 0 || i > maxNumberLine {
		return 0, false
	}
	n, ok := parseInt(buf[*pos : *pos+i])
	if !ok || n < -1 || n > math.MaxInt32 {
		return 0, false
	}
	*pos += i + 2
	return int(n), true
}
