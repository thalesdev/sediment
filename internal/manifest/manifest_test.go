// SPDX-License-Identifier: MIT

package manifest

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/thalesdev/sediment/internal/ikey"
)

func key(user string, seq uint64) []byte {
	return ikey.Append(nil, []byte(user), seq, ikey.KindPut)
}

func meta(level int, number uint64) TableMeta {
	return TableMeta{Level: level, Number: number, Size: number * 100, Smallest: key("a", number), Largest: key("z", number)}
}

func assertCorrupt(t *testing.T, data []byte) {
	t.Helper()
	if _, err := Decode(data); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Decode(% x) = %v, want ErrCorrupt", data, err)
	}
}

func TestEncodeDecode(t *testing.T) {
	t.Run("Should write the tags of the LevelDB MANIFEST", func(t *testing.T) {
		e := Edit{
			HasLogNumber: true, LogNumber: 5,
			HasNextFileNumber: true, NextFileNumber: 300,
			HasLastSeq: true, LastSeq: 9,
			Delete: []TableRef{{Level: 1, Number: 7}},
			Add:    []TableMeta{{Level: 0, Number: 8, Size: 200, Smallest: []byte("ab"), Largest: []byte("cd")}},
		}
		want := []byte{
			2, 5,
			3, 0xac, 0x02,
			4, 9,
			6, 1, 7,
			7, 0, 8, 0xc8, 0x01, 2, 'a', 'b', 2, 'c', 'd',
		}
		if got := e.Encode(nil); !bytes.Equal(got, want) {
			t.Fatalf("got % x, want % x", got, want)
		}
	})

	t.Run("Should round trip every kind of field", func(t *testing.T) {
		cases := map[string]Edit{
			"log number":       {HasLogNumber: true, LogNumber: 0},
			"next file number": {HasNextFileNumber: true, NextFileNumber: math.MaxUint64},
			"last seq":         {HasLastSeq: true, LastSeq: ikey.MaxSeq},
			"delete":           {Delete: []TableRef{{0, 1}, {MaxLevel, math.MaxUint64}}},
			"add":              {Add: []TableMeta{meta(0, 3), meta(MaxLevel, 9)}},
			"everything": {
				HasLogNumber: true, LogNumber: 4, HasNextFileNumber: true, NextFileNumber: 10, HasLastSeq: true, LastSeq: 77,
				Delete: []TableRef{{0, 1}}, Add: []TableMeta{meta(0, 5)},
			},
		}
		for name, e := range cases {
			got, err := Decode(e.Encode(nil))
			if err != nil || !reflect.DeepEqual(got, e) {
				t.Fatalf("%s: got %+v, %v, want %+v", name, got, err, e)
			}
		}
	})

	t.Run("Should not use the memory of the data that it decodes", func(t *testing.T) {
		data := Edit{Add: []TableMeta{meta(0, 3)}}.Encode(nil)
		e, err := Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		want := bytes.Clone(e.Add[0].Smallest)
		for i := range data {
			data[i] = 0xff
		}
		if !bytes.Equal(e.Add[0].Smallest, want) {
			t.Fatal("the edit points into the data")
		}
	})
}

func TestDecodeCorruption(t *testing.T) {
	t.Run("Should reject an edit with no field", func(t *testing.T) {
		assertCorrupt(t, nil)
		assertCorrupt(t, []byte{})
	})

	t.Run("Should reject a tag that is not known", func(t *testing.T) {
		for _, tag := range []byte{0, 1, 5, 8, 9, 10, 0x7f} {
			assertCorrupt(t, []byte{tag, 1})
		}
		assertCorrupt(t, []byte{2, 1, 99, 1})
	})

	t.Run("Should reject a field that appears two times", func(t *testing.T) {
		assertCorrupt(t, []byte{2, 1, 2, 2})
		assertCorrupt(t, []byte{3, 1, 3, 2})
		assertCorrupt(t, []byte{4, 1, 4, 2})
	})

	t.Run("Should reject a value that is not valid", func(t *testing.T) {
		assertCorrupt(t, []byte{2})
		assertCorrupt(t, []byte{2, 0x80})
		assertCorrupt(t, Edit{HasLastSeq: true, LastSeq: ikey.MaxSeq + 1}.Encode(nil))
		assertCorrupt(t, []byte{6, 7, 1})
		assertCorrupt(t, []byte{7, 9, 1, 1, 1, 'a', 1, 'b'})
		assertCorrupt(t, []byte{7, 0, 1, 1, 0, 1, 'b'})
		assertCorrupt(t, []byte{7, 0, 1, 1, 1, 'a', 0})
	})

	t.Run("Should reject a cut edit and a length that does not fit", func(t *testing.T) {
		data := Edit{HasLogNumber: true, LogNumber: 5, Delete: []TableRef{{1, 7}}, Add: []TableMeta{meta(0, 3)}}.Encode(nil)
		for n := 1; n < len(data); n++ {
			if _, err := Decode(data[:n]); err == nil {
				if n != 2 && n != 5 {
					t.Fatalf("Decode accepted an edit that is cut to %d of %d bytes", n, len(data))
				}
			} else if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("got %v, want ErrCorrupt", err)
			}
		}
		assertCorrupt(t, []byte{7, 0, 1, 1, 200, 'a'})
		assertCorrupt(t, append([]byte{7, 0, 1, 1}, append([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}, 'a')...))
	})
}

func TestVersion(t *testing.T) {
	t.Run("Should apply a sequence of edits", func(t *testing.T) {
		var v Version
		edits := []Edit{
			{HasLogNumber: true, LogNumber: 2, HasNextFileNumber: true, NextFileNumber: 4, HasLastSeq: true, LastSeq: 10, Add: []TableMeta{meta(0, 3)}},
			{HasLogNumber: true, LogNumber: 5, HasNextFileNumber: true, NextFileNumber: 7, HasLastSeq: true, LastSeq: 20, Add: []TableMeta{meta(0, 6)}},
			{Add: []TableMeta{meta(0, 4)}},
			{HasLastSeq: true, LastSeq: 30, Delete: []TableRef{{0, 3}}, Add: []TableMeta{meta(1, 8)}},
		}
		for _, e := range edits {
			if err := v.Apply(e); err != nil {
				t.Fatal(err)
			}
		}
		if v.LogNumber != 5 || v.NextFileNumber != 7 || v.LastSeq != 30 {
			t.Fatalf("scalars are %d %d %d", v.LogNumber, v.NextFileNumber, v.LastSeq)
		}
		var level0 []uint64
		for _, tbl := range v.Tables[0] {
			level0 = append(level0, tbl.Number)
		}
		if !reflect.DeepEqual(level0, []uint64{4, 6}) || len(v.Tables[1]) != 1 || v.Tables[1][0].Number != 8 {
			t.Fatalf("tables are %v and %v", level0, v.Tables[1])
		}
	})

	t.Run("Should leave the version as it was when an edit is not valid", func(t *testing.T) {
		var v Version
		if err := v.Apply(Edit{HasLogNumber: true, LogNumber: 2, Add: []TableMeta{meta(0, 3)}}); err != nil {
			t.Fatal(err)
		}
		before := v.Compact()
		bad := []Edit{
			{HasLogNumber: true, LogNumber: 9, Add: []TableMeta{meta(0, 3)}},
			{HasLogNumber: true, LogNumber: 9, Add: []TableMeta{meta(1, 3)}},
			{HasLogNumber: true, LogNumber: 9, Delete: []TableRef{{0, 4}}},
			{HasLogNumber: true, LogNumber: 9, Delete: []TableRef{{1, 3}}},
			{HasLogNumber: true, LogNumber: 9, Add: []TableMeta{meta(0, 5), meta(0, 5)}},
		}
		for i, e := range bad {
			if err := v.Apply(e); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("edit %d: got %v, want ErrCorrupt", i, err)
			}
			if !reflect.DeepEqual(v.Compact(), before) {
				t.Fatalf("edit %d changed the version", i)
			}
		}
	})

	t.Run("Should build the same version from Compact", func(t *testing.T) {
		var v Version
		for _, e := range []Edit{
			{HasLogNumber: true, LogNumber: 2, HasNextFileNumber: true, NextFileNumber: 9, HasLastSeq: true, LastSeq: 12, Add: []TableMeta{meta(0, 3), meta(0, 5)}},
			{Add: []TableMeta{meta(2, 7)}},
		} {
			if err := v.Apply(e); err != nil {
				t.Fatal(err)
			}
		}
		var w Version
		if err := w.Apply(v.Compact()); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(v, w) {
			t.Fatalf("got %+v, want %+v", w, v)
		}
		decoded, err := Decode(v.Compact().Encode(nil))
		if err != nil {
			t.Fatal(err)
		}
		var x Version
		if err := x.Apply(decoded); err != nil || !reflect.DeepEqual(v, x) {
			t.Fatalf("got %+v, %v after a round trip", x, err)
		}
	})
}

func FuzzDecode(f *testing.F) {
	f.Add(Edit{HasLogNumber: true, LogNumber: 5, Delete: []TableRef{{1, 7}}, Add: []TableMeta{meta(0, 3)}}.Encode(nil))
	f.Add([]byte{})
	f.Add([]byte{7, 0, 1, 1, 1, 'a', 1, 'b'})
	f.Fuzz(func(t *testing.T, data []byte) {
		e, err := Decode(data)
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("got %v, want ErrCorrupt", err)
			}
			return
		}
		again, err := Decode(e.Encode(nil))
		if err != nil || !reflect.DeepEqual(again, e) {
			t.Fatalf("round trip gave %+v, %v, want %+v", again, err, e)
		}
		var v Version
		_ = v.Apply(e)
	})
}
