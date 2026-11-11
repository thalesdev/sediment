// SPDX-License-Identifier: MIT

package batch

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/thalesdev/sediment/internal/ikey"
)

type op struct {
	seq   uint64
	kind  ikey.Kind
	key   []byte
	value []byte
}

func collect(v View) []op {
	var out []op
	v.ForEach(func(seq uint64, kind ikey.Kind, key, value []byte) {
		out = append(out, op{seq, kind, bytes.Clone(key), bytes.Clone(value)})
	})
	return out
}

func header(seq uint64, count uint32) []byte {
	return binary.LittleEndian.AppendUint32(binary.LittleEndian.AppendUint64(nil, seq), count)
}

func assertCorrupt(t *testing.T, data []byte) {
	t.Helper()
	if _, err := Parse(data); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Parse(% x) = %v, want ErrCorrupt", data, err)
	}
}

func TestBatch(t *testing.T) {
	t.Run("Should write the format of the LevelDB WriteBatch", func(t *testing.T) {
		var b Batch
		b.Reset(1)
		b.Put([]byte("a"), []byte("b"))
		b.Delete([]byte("c"))
		want := []byte{
			1, 0, 0, 0, 0, 0, 0, 0,
			2, 0, 0, 0,
			1, 1, 'a', 1, 'b',
			0, 1, 'c',
		}
		if !bytes.Equal(b.Bytes(), want) {
			t.Fatalf("got % x, want % x", b.Bytes(), want)
		}
		if b.Count() != 2 || b.Seq() != 1 {
			t.Fatalf("Count=%d Seq=%d", b.Count(), b.Seq())
		}
	})

	t.Run("Should round trip operations with empty and long keys and values", func(t *testing.T) {
		long := bytes.Repeat([]byte{0xab}, 70000)
		var b Batch
		b.Reset(41)
		b.Put(nil, nil)
		b.Put([]byte("key"), long)
		b.Delete(long)
		b.Delete(nil)
		b.Put(long, []byte("v"))
		v, err := Parse(b.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		got := collect(v)
		want := []op{
			{41, ikey.KindPut, nil, nil},
			{42, ikey.KindPut, []byte("key"), long},
			{43, ikey.KindDelete, long, nil},
			{44, ikey.KindDelete, nil, nil},
			{45, ikey.KindPut, long, []byte("v")},
		}
		if v.Seq() != 41 || v.Count() != 5 || len(got) != len(want) {
			t.Fatalf("Seq=%d Count=%d ops=%d", v.Seq(), v.Count(), len(got))
		}
		for i := range want {
			if got[i].seq != want[i].seq || got[i].kind != want[i].kind || !bytes.Equal(got[i].key, want[i].key) || !bytes.Equal(got[i].value, want[i].value) {
				t.Fatalf("operation %d differs", i)
			}
		}
	})

	t.Run("Should reuse the buffer and clear the old operations at Reset", func(t *testing.T) {
		var b Batch
		b.Reset(1)
		b.Put([]byte("old"), []byte("old"))
		b.Reset(9)
		b.Delete([]byte("k"))
		v, err := Parse(b.Bytes())
		if err != nil || v.Seq() != 9 || v.Count() != 1 {
			t.Fatalf("Parse = %+v, %v", v, err)
		}
	})

	t.Run("Should be ready for use as a zero value", func(t *testing.T) {
		var b Batch
		b.Put([]byte("k"), []byte("v"))
		if v, err := Parse(b.Bytes()); err != nil || v.Seq() != 0 || v.Count() != 1 {
			t.Fatalf("Parse = %+v, %v", v, err)
		}
	})
}

func TestParseCorruption(t *testing.T) {
	good := func() []byte {
		var b Batch
		b.Reset(5)
		b.Put([]byte("key"), []byte("value"))
		b.Delete([]byte("gone"))
		return bytes.Clone(b.Bytes())
	}

	t.Run("Should reject a header that is short", func(t *testing.T) {
		for n := range HeaderSize {
			assertCorrupt(t, good()[:n])
		}
	})

	t.Run("Should reject a batch with no operation", func(t *testing.T) {
		assertCorrupt(t, header(1, 0))
	})

	t.Run("Should reject sequence numbers past the largest one", func(t *testing.T) {
		assertCorrupt(t, append(header(ikey.MaxSeq+1, 1), 0, 0))
		assertCorrupt(t, append(header(math.MaxUint64, 1), 0, 0))
		assertCorrupt(t, append(header(ikey.MaxSeq, 2), 0, 0, 0, 0))
		if _, err := Parse(append(header(ikey.MaxSeq, 1), 0, 0)); err != nil {
			t.Fatalf("the largest sequence number must be valid: %v", err)
		}
	})

	t.Run("Should reject a kind that is not valid", func(t *testing.T) {
		for _, kind := range []byte{2, 3, 0xff} {
			assertCorrupt(t, append(header(1, 1), kind, 0, 0))
		}
	})

	t.Run("Should reject a batch that is cut at any length", func(t *testing.T) {
		data := good()
		for n := HeaderSize; n < len(data); n++ {
			assertCorrupt(t, data[:n])
		}
	})

	t.Run("Should reject a length that is not valid or that does not fit", func(t *testing.T) {
		assertCorrupt(t, append(header(1, 1), 1, 0x80))
		assertCorrupt(t, append(header(1, 1), 1, 5, 'a'))
		assertCorrupt(t, append(header(1, 1), 1, 1, 'a', 9, 'b'))
		assertCorrupt(t, append(header(1, 1), append([]byte{1}, binary.AppendUvarint(nil, math.MaxUint64)...)...))
		assertCorrupt(t, append(header(1, 1), append([]byte{0}, binary.AppendUvarint(nil, 1<<63)...)...))
	})

	t.Run("Should reject a count that is not the number of operations", func(t *testing.T) {
		data := good()
		binary.LittleEndian.PutUint32(data[8:], 3)
		assertCorrupt(t, data)
		binary.LittleEndian.PutUint32(data[8:], 1)
		assertCorrupt(t, data)
	})

	t.Run("Should reject a huge count without a large allocation", func(t *testing.T) {
		assertCorrupt(t, append(header(1, math.MaxUint32), 0, 0))
	})

	t.Run("Should reject bytes after the last operation", func(t *testing.T) {
		assertCorrupt(t, append(good(), 0))
	})
}

func FuzzParse(f *testing.F) {
	var b Batch
	b.Reset(3)
	b.Put([]byte("key"), []byte("value"))
	b.Delete([]byte("gone"))
	f.Add(bytes.Clone(b.Bytes()))
	f.Add([]byte{})
	f.Add(header(1, 1))
	f.Fuzz(func(t *testing.T, data []byte) {
		v, err := Parse(data)
		if err != nil {
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("got %v, want ErrCorrupt", err)
			}
			return
		}
		ops := collect(v)
		if len(ops) != v.Count() {
			t.Fatalf("ForEach gave %d operations, Count is %d", len(ops), v.Count())
		}
		var again Batch
		again.Reset(v.Seq())
		for _, o := range ops {
			if o.kind == ikey.KindPut {
				again.Put(o.key, o.value)
			} else {
				again.Delete(o.key)
			}
		}
		v2, err := Parse(again.Bytes())
		if err != nil {
			t.Fatalf("a batch that was built from a valid batch is not valid: %v", err)
		}
		ops2 := collect(v2)
		if len(ops2) != len(ops) {
			t.Fatalf("round trip gave %d operations, want %d", len(ops2), len(ops))
		}
		for i := range ops {
			if ops[i].seq != ops2[i].seq || ops[i].kind != ops2[i].kind || !bytes.Equal(ops[i].key, ops2[i].key) || !bytes.Equal(ops[i].value, ops2[i].value) {
				t.Fatalf("operation %d differs after a round trip", i)
			}
		}
	})
}
