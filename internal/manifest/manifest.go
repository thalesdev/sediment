// SPDX-License-Identifier: MIT

package manifest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"github.com/thalesdev/sediment/internal/ikey"
)

const (
	tagLogNumber      = 2
	tagNextFileNumber = 3
	tagLastSeq        = 4
	tagDeleteTable    = 6
	tagAddTable       = 7
	tagAddTableSeq    = 100

	// MaxLevel is the highest level that an edit can name.
	MaxLevel = 6
)

// ErrCorrupt is the base of all errors that Decode and Apply return.
var ErrCorrupt = errors.New("manifest: corrupt edit")

// TableMeta describes one table file.
type TableMeta struct {
	// Level is the level of the table.
	Level int

	// Number is the file number of the table.
	Number uint64

	// Size is the size of the file in bytes.
	Size uint64

	// Smallest is the smallest internal key of the table.
	Smallest []byte

	// Largest is the largest internal key of the table.
	Largest []byte

	// HasSeq tells if SmallestSeq and LargestSeq are known. A table that an
	// edit adds with the tag of the LevelDB has no sequence range.
	HasSeq bool

	// SmallestSeq is the smallest sequence number that the table can hold.
	SmallestSeq uint64

	// LargestSeq is the largest sequence number that the table can hold.
	LargestSeq uint64
}

// TableRef names one table file.
type TableRef struct {
	// Level is the level of the table.
	Level int

	// Number is the file number of the table.
	Number uint64
}

// Edit is one change of the state. A field with a Has flag is part of the edit
// only if the flag is true.
type Edit struct {
	// HasLogNumber tells if LogNumber is part of the edit.
	HasLogNumber bool

	// LogNumber is the number of the first log file that is not in a table. A
	// log file with a lower number is not read again.
	LogNumber uint64

	// HasNextFileNumber tells if NextFileNumber is part of the edit.
	HasNextFileNumber bool

	// NextFileNumber is the number for the next file that the database makes.
	NextFileNumber uint64

	// HasLastSeq tells if LastSeq is part of the edit.
	HasLastSeq bool

	// LastSeq is the largest sequence number that the tables hold, or a larger
	// one.
	LastSeq uint64

	// Delete lists the tables that the edit removes. Apply does them first.
	Delete []TableRef

	// Add lists the tables that the edit adds.
	Add []TableMeta
}

// Encode appends the encoded edit to dst and returns the result.
func (e Edit) Encode(dst []byte) []byte {
	if e.HasLogNumber {
		dst = binary.AppendUvarint(binary.AppendUvarint(dst, tagLogNumber), e.LogNumber)
	}
	if e.HasNextFileNumber {
		dst = binary.AppendUvarint(binary.AppendUvarint(dst, tagNextFileNumber), e.NextFileNumber)
	}
	if e.HasLastSeq {
		dst = binary.AppendUvarint(binary.AppendUvarint(dst, tagLastSeq), e.LastSeq)
	}
	for _, d := range e.Delete {
		dst = binary.AppendUvarint(dst, tagDeleteTable)
		dst = binary.AppendUvarint(dst, uint64(d.Level))
		dst = binary.AppendUvarint(dst, d.Number)
	}
	for _, a := range e.Add {
		tag := uint64(tagAddTable)
		if a.HasSeq {
			tag = tagAddTableSeq
		}
		dst = binary.AppendUvarint(dst, tag)
		dst = binary.AppendUvarint(dst, uint64(a.Level))
		dst = binary.AppendUvarint(dst, a.Number)
		dst = binary.AppendUvarint(dst, a.Size)
		dst = binary.AppendUvarint(dst, uint64(len(a.Smallest)))
		dst = append(dst, a.Smallest...)
		dst = binary.AppendUvarint(dst, uint64(len(a.Largest)))
		dst = append(dst, a.Largest...)
		if a.HasSeq {
			dst = binary.AppendUvarint(dst, a.SmallestSeq)
			dst = binary.AppendUvarint(dst, a.LargestSeq)
		}
	}
	return dst
}

// decoder reads the values of an edit and keeps the first error.
type decoder struct {
	data []byte
	pos  int
	err  error
}

// fail stores a corruption error, if there is none yet.
func (d *decoder) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
	}
}

// uvarint reads a varint.
func (d *decoder) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.data[d.pos:])
	if n <= 0 {
		d.fail("varint at %d is not valid", d.pos)
		return 0
	}
	d.pos += n
	return v
}

// level reads a level and checks it.
func (d *decoder) level() int {
	v := d.uvarint()
	if v > MaxLevel {
		d.fail("level %d is not valid", v)
		return 0
	}
	return int(v)
}

// bytes reads a length and then that number of bytes. The result is a copy.
func (d *decoder) bytes() []byte {
	length := d.uvarint()
	if d.err != nil {
		return nil
	}
	if length == 0 {
		d.fail("key at %d is empty", d.pos)
		return nil
	}
	if length > uint64(len(d.data)-d.pos) {
		d.fail("%d bytes do not fit in the %d bytes that are left", length, len(d.data)-d.pos)
		return nil
	}
	b := bytes.Clone(d.data[d.pos : d.pos+int(length)])
	d.pos += int(length)
	return b
}

// Decode reads an edit from data. It returns an error that wraps ErrCorrupt for
// an edit that has no field, for a tag that is not known, for a field that
// appears two times, for a value that is not valid, and for bytes that are cut
// or that follow the last field. The edit does not use the memory of data.
func Decode(data []byte) (Edit, error) {
	var e Edit
	d := &decoder{data: data}
	if len(data) == 0 {
		d.fail("edit has no field")
	}
	for d.err == nil && d.pos < len(data) {
		tag := d.uvarint()
		switch tag {
		case tagLogNumber:
			if e.HasLogNumber {
				d.fail("log number appears two times")
			}
			e.HasLogNumber, e.LogNumber = true, d.uvarint()
		case tagNextFileNumber:
			if e.HasNextFileNumber {
				d.fail("next file number appears two times")
			}
			e.HasNextFileNumber, e.NextFileNumber = true, d.uvarint()
		case tagLastSeq:
			if e.HasLastSeq {
				d.fail("last sequence number appears two times")
			}
			e.HasLastSeq, e.LastSeq = true, d.uvarint()
			if e.LastSeq > ikey.MaxSeq {
				d.fail("last sequence number %d is too large", e.LastSeq)
			}
		case tagDeleteTable:
			ref := TableRef{Level: d.level(), Number: d.uvarint()}
			e.Delete = append(e.Delete, ref)
		case tagAddTable, tagAddTableSeq:
			meta := TableMeta{Level: d.level(), Number: d.uvarint(), Size: d.uvarint()}
			meta.Smallest = d.bytes()
			meta.Largest = d.bytes()
			if tag == tagAddTableSeq {
				meta.HasSeq = true
				meta.SmallestSeq = d.uvarint()
				meta.LargestSeq = d.uvarint()
				if meta.LargestSeq > ikey.MaxSeq || meta.SmallestSeq > meta.LargestSeq {
					d.fail("sequence range %d to %d is not valid", meta.SmallestSeq, meta.LargestSeq)
				}
			}
			e.Add = append(e.Add, meta)
		default:
			d.fail("tag %d is not known", tag)
		}
	}
	if d.err != nil {
		return Edit{}, d.err
	}
	return e, nil
}

// Version is the state that a sequence of edits builds. The zero value is the
// empty state.
type Version struct {
	// LogNumber is the number of the first log file that is not in a table.
	LogNumber uint64

	// NextFileNumber is the number for the next file that the database makes.
	NextFileNumber uint64

	// LastSeq is the largest sequence number that the tables hold, or a larger
	// one.
	LastSeq uint64

	// Tables holds the tables of each level, in increasing order of number.
	Tables [MaxLevel + 1][]TableMeta
}

// Apply changes the version with e. It deletes the tables of e.Delete first, and
// then it adds the tables of e.Add. It returns an error that wraps ErrCorrupt,
// and leaves the version as it was, if the edit deletes a table that does not
// exist or adds a table with a number that already exists.
func (v *Version) Apply(e Edit) error {
	next := *v
	for level := range next.Tables {
		next.Tables[level] = slices.Clone(v.Tables[level])
	}
	for _, d := range e.Delete {
		i := slices.IndexFunc(next.Tables[d.Level], func(t TableMeta) bool { return t.Number == d.Number })
		if i < 0 {
			return fmt.Errorf("%w: edit deletes table %d of level %d, which does not exist", ErrCorrupt, d.Number, d.Level)
		}
		next.Tables[d.Level] = slices.Delete(next.Tables[d.Level], i, i+1)
	}
	for _, a := range e.Add {
		if next.has(a.Number) {
			return fmt.Errorf("%w: edit adds table %d, which already exists", ErrCorrupt, a.Number)
		}
		level := next.Tables[a.Level]
		i, _ := slices.BinarySearchFunc(level, a.Number, func(t TableMeta, n uint64) int {
			switch {
			case t.Number < n:
				return -1
			case t.Number > n:
				return 1
			}
			return 0
		})
		next.Tables[a.Level] = slices.Insert(level, i, a)
	}
	if e.HasLogNumber {
		next.LogNumber = e.LogNumber
	}
	if e.HasNextFileNumber {
		next.NextFileNumber = e.NextFileNumber
	}
	if e.HasLastSeq {
		next.LastSeq = e.LastSeq
	}
	*v = next
	return nil
}

// has tells if a table with number exists in any level.
func (v *Version) has(number uint64) bool {
	for _, level := range v.Tables {
		if slices.ContainsFunc(level, func(t TableMeta) bool { return t.Number == number }) {
			return true
		}
	}
	return false
}

// Compact returns one edit that builds the version from the empty state.
func (v *Version) Compact() Edit {
	e := Edit{
		HasLogNumber:      true,
		LogNumber:         v.LogNumber,
		HasNextFileNumber: true,
		NextFileNumber:    v.NextFileNumber,
		HasLastSeq:        true,
		LastSeq:           v.LastSeq,
	}
	for _, level := range v.Tables {
		e.Add = append(e.Add, level...)
	}
	return e
}
