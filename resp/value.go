// SPDX-License-Identifier: MIT

package resp

// Kind is the type of a Value.
type Kind uint8

const (
	// KindSimple is a simple string, +OK.
	KindSimple Kind = iota + 1

	// KindError is a simple error, -ERR message.
	KindError

	// KindInt is a signed 64-bit integer.
	KindInt

	// KindBulk is a binary-safe string.
	KindBulk

	// KindNull is the absence of a value.
	KindNull

	// KindArray is an ordered list of values.
	KindArray

	// KindBool is a boolean, RESP3.
	KindBool

	// KindDouble is a floating point number, RESP3.
	KindDouble

	// KindBigNumber is an integer of any size, as decimal text, RESP3.
	KindBigNumber

	// KindVerbatim is a string with a three letter format, RESP3.
	KindVerbatim

	// KindBulkError is an error with binary-safe text, RESP3.
	KindBulkError

	// KindMap is a list of pairs, RESP3. Elems holds key, value, key, value.
	KindMap

	// KindSet is an unordered list of unique values, RESP3.
	KindSet

	// KindPush is a message that the server sends without a request, RESP3.
	KindPush

	// KindNullArray is the absence of an array: *-1 in RESP2 and _ in RESP3. EXEC
	// replies with it when a watched key changed.
	KindNullArray
)

// Value is a RESP value. It does not depend on the version of the protocol. The
// Writer chooses the bytes for each version.
type Value struct {
	// Kind is the type of the value.
	Kind Kind

	// Bytes is the text of a simple string, an error, a bulk string, a big
	// number, a verbatim string or a bulk error.
	Bytes []byte

	// Int is the number of a KindInt value.
	Int int64

	// Double is the number of a KindDouble value.
	Double float64

	// Bool is the value of a KindBool value.
	Bool bool

	// Format is the three letter format of a KindVerbatim value, for example
	// "txt".
	Format string

	// Elems are the values inside an array, a set or a push, and the keys and
	// values of a map.
	Elems []Value
}

// Simple returns a simple string value.
func Simple(s string) Value {
	return Value{Kind: KindSimple, Bytes: []byte(s)}
}

// Error returns a simple error value. By convention s starts with an upper case
// code, as in "ERR something".
func Error(s string) Value {
	return Value{Kind: KindError, Bytes: []byte(s)}
}

// Integer returns an integer value.
func Integer(n int64) Value {
	return Value{Kind: KindInt, Int: n}
}

// Bulk returns a bulk string value. It does not copy b.
func Bulk(b []byte) Value {
	return Value{Kind: KindBulk, Bytes: b}
}

// BulkString returns a bulk string value with the bytes of s.
func BulkString(s string) Value {
	return Value{Kind: KindBulk, Bytes: []byte(s)}
}

// NullArray returns the absence of an array.
func NullArray() Value {
	return Value{Kind: KindNullArray}
}

// Null returns the absence of a value.
func Null() Value {
	return Value{Kind: KindNull}
}

// Array returns an array value.
func Array(elems ...Value) Value {
	return Value{Kind: KindArray, Elems: elems}
}

// Map returns a map value. kv holds key, value, key, value and so on.
func Map(kv ...Value) Value {
	return Value{Kind: KindMap, Elems: kv}
}
