// SPDX-License-Identifier: MIT

package resp

import (
	"bytes"
	"strings"
)

// readInline reads an inline command: one line of words separated by spaces,
// with optional quotes. It returns ErrCrossProtocol if the line looks like the
// first line of an HTTP request.
func (r *Reader) readInline() ([][]byte, error) {
	line, err := r.readLine(r.limits.MaxInline, true)
	if err != nil {
		if pe, ok := err.(*ProtocolError); ok && pe.Msg == "line too long" {
			return nil, protocolError("too big inline request")
		}
		return nil, err
	}
	args, ok := splitArgs(line, r.limits.MaxArgs)
	if !ok {
		return nil, protocolError("unbalanced quotes in request")
	}
	if len(args) > r.limits.MaxArgs {
		return nil, protocolError("invalid multibulk length")
	}
	if len(args) > 0 && (strings.EqualFold(string(args[0]), "post") || strings.EqualFold(string(args[0]), "host:")) {
		return nil, ErrCrossProtocol
	}
	return args, nil
}

// splitArgs splits line into words. A word can be in double quotes, where the
// escapes \n \r \t \b \a \\ \" and \xHH work, or in single quotes, where only \'
// works. It stops after max+1 words. It returns false if a quote is not closed
// or if a closing quote is not followed by a space.
func splitArgs(line []byte, max int) ([][]byte, bool) {
	var args [][]byte
	i := 0
	for {
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			return args, true
		}
		if len(args) > max {
			return args, true
		}
		var word []byte
		var ok bool
		switch line[i] {
		case '"':
			word, i, ok = splitDouble(line, i+1)
		case '\'':
			word, i, ok = splitSingle(line, i+1)
		default:
			start := i
			for i < len(line) && !isSpace(line[i]) {
				i++
			}
			word, ok = bytes.Clone(line[start:i]), true
		}
		if !ok {
			return nil, false
		}
		args = append(args, word)
	}
}

// splitDouble reads a word in double quotes. i is the index after the opening
// quote. It returns the word and the index after the closing quote.
func splitDouble(line []byte, i int) ([]byte, int, bool) {
	word := []byte{}
	for i < len(line) {
		c := line[i]
		switch {
		case c == '\\' && i+3 < len(line) && line[i+1] == 'x' && isHex(line[i+2]) && isHex(line[i+3]):
			word = append(word, hexValue(line[i+2])<<4|hexValue(line[i+3]))
			i += 4
		case c == '\\' && i+1 < len(line):
			word = append(word, unescape(line[i+1]))
			i += 2
		case c == '"':
			if i+1 < len(line) && !isSpace(line[i+1]) {
				return nil, 0, false
			}
			return word, i + 1, true
		default:
			word = append(word, c)
			i++
		}
	}
	return nil, 0, false
}

// splitSingle reads a word in single quotes. i is the index after the opening
// quote. It returns the word and the index after the closing quote.
func splitSingle(line []byte, i int) ([]byte, int, bool) {
	word := []byte{}
	for i < len(line) {
		c := line[i]
		switch {
		case c == '\\' && i+1 < len(line) && line[i+1] == '\'':
			word = append(word, '\'')
			i += 2
		case c == '\'':
			if i+1 < len(line) && !isSpace(line[i+1]) {
				return nil, 0, false
			}
			return word, i + 1, true
		default:
			word = append(word, c)
			i++
		}
	}
	return nil, 0, false
}

// unescape returns the byte that the escape \c stands for.
func unescape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	case 'b':
		return '\b'
	case 'a':
		return '\a'
	}
	return c
}

// isSpace reports whether c separates words.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}

// isHex reports whether c is a hexadecimal digit.
func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// hexValue returns the value of the hexadecimal digit c.
func hexValue(c byte) byte {
	switch {
	case c >= 'a':
		return c - 'a' + 10
	case c >= 'A':
		return c - 'A' + 10
	}
	return c - '0'
}
