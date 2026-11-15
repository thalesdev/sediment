// SPDX-License-Identifier: MIT

package resp

const (
	// DefaultMaxBulk is the default size of a bulk string, 16 MiB.
	DefaultMaxBulk = 16 << 20

	// DefaultMaxArgs is the default number of elements of an array and of
	// arguments of a command.
	DefaultMaxArgs = 1024

	// DefaultMaxInline is the default size of an inline command or of a simple
	// string, 64 KiB.
	DefaultMaxInline = 64 << 10

	// DefaultMaxDepth is the default depth of nested aggregates.
	DefaultMaxDepth = 32

	// DefaultMaxNodes is the default number of values in one decoded value.
	DefaultMaxNodes = 1 << 16

	// DefaultMaxRequest is the default total size of the bulk strings of one
	// command, 32 MiB.
	DefaultMaxRequest = 32 << 20
)

// Limits are the bounds that the Reader enforces. A field that is 0 or negative
// takes its default.
type Limits struct {
	// MaxBulk is the largest size of one bulk string, in bytes.
	MaxBulk int

	// MaxArgs is the largest number of elements of one array, map or set, and
	// the largest number of arguments of one command.
	MaxArgs int

	// MaxInline is the largest size of one inline command line and of one
	// simple string, simple error or number line.
	MaxInline int

	// MaxDepth is the deepest nesting of aggregates in a value.
	MaxDepth int

	// MaxNodes is the largest number of values in one value read by ReadValue,
	// counting the nested ones.
	MaxNodes int

	// MaxRequest is the largest total size of the bulk strings of one command.
	MaxRequest int
}

// DefaultLimits returns the default limits.
func DefaultLimits() Limits {
	return Limits{}.withDefaults()
}

// withDefaults returns l with the default in each field that is not set.
func (l Limits) withDefaults() Limits {
	if l.MaxBulk <= 0 {
		l.MaxBulk = DefaultMaxBulk
	}
	if l.MaxArgs <= 0 {
		l.MaxArgs = DefaultMaxArgs
	}
	if l.MaxInline <= 0 {
		l.MaxInline = DefaultMaxInline
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	if l.MaxNodes <= 0 {
		l.MaxNodes = DefaultMaxNodes
	}
	if l.MaxRequest <= 0 {
		l.MaxRequest = DefaultMaxRequest
	}
	return l
}
