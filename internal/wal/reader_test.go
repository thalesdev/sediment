package wal

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"
)

var errRead = errors.New("read failed")

type failingReader struct {
	data  []byte
	limit int
	pos   int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.pos >= f.limit {
		return 0, errRead
	}
	n := copy(p, f.data[f.pos:min(f.limit, len(f.data))])
	f.pos += n
	return n, nil
}

func readAll(t *testing.T, b []byte) ([][]byte, *Reader) {
	t.Helper()
	r := NewReader(bytes.NewReader(b))
	var out [][]byte
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			return out, r
		}
		if err != nil {
			t.Fatalf("Next: unexpected error %v", err)
		}
		out = append(out, append([]byte{}, rec...))
	}
}

func rawFragment(typ recordType, payload []byte) []byte {
	h := newHeader(payload, typ)
	buf := make([]byte, headerSize)
	h.marshal(buf)
	return append(buf, payload...)
}

func writeWithEnds(t *testing.T, records [][]byte) ([]byte, []int) {
	t.Helper()
	dst := &fakeDst{}
	w := NewWriter(dst)
	ends := make([]int, len(records))
	for i, rec := range records {
		if err := w.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord %d: %v", i, err)
		}
		ends[i] = dst.buf.Len()
	}
	return dst.buf.Bytes(), ends
}

func variedRecords(seed uint64, n int) [][]byte {
	rng := rand.New(rand.NewPCG(seed, seed))
	sizes := []int{
		0, 1, 100,
		blockSize - 2*headerSize,
		0,
		maxPayloadSize - 1, maxPayloadSize, maxPayloadSize + 1,
		2*blockSize + 17,
	}
	for range n {
		sizes = append(sizes, rng.IntN(3*blockSize))
	}
	records := make([][]byte, len(sizes))
	for i, s := range sizes {
		records[i] = payloadOf(s, byte(i))
	}
	return records
}

func assertRecords(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("record %d (size %d) differs: got size %d", i, len(want[i]), len(got[i]))
		}
	}
}

func TestReaderRoundTrip(t *testing.T) {
	t.Run("Should return io.EOF on empty log", func(t *testing.T) {
		got, r := readAll(t, nil)
		if len(got) != 0 || r.Dropped() != 0 {
			t.Fatalf("got %d records and %d dropped, want 0 and 0", len(got), r.Dropped())
		}
	})

	t.Run("Should read back records of varied sizes written by the writer", func(t *testing.T) {
		records := variedRecords(1, 60)
		b, _ := writeWithEnds(t, records)
		got, r := readAll(t, b)
		assertRecords(t, got, records)
		if r.Dropped() != 0 {
			t.Fatalf("got %d dropped on a clean log, want 0", r.Dropped())
		}
	})

	t.Run("Should read records around seven byte and trailer boundaries", func(t *testing.T) {
		for leftover := 0; leftover <= headerSize; leftover++ {
			records := [][]byte{payloadOf(blockSize-headerSize-leftover, 1), []byte("abc"), {}, []byte("z")}
			b, _ := writeWithEnds(t, records)
			got, r := readAll(t, b)
			assertRecords(t, got, records)
			if r.Dropped() != 0 {
				t.Fatalf("leftover %d: got %d dropped, want 0", leftover, r.Dropped())
			}
		}
	})

	t.Run("Should report zero dropped when log ends exactly at a block boundary", func(t *testing.T) {
		records := [][]byte{payloadOf(maxPayloadSize, 2)}
		b, _ := writeWithEnds(t, records)
		if len(b) != blockSize {
			t.Fatalf("setup: got %d bytes, want %d", len(b), blockSize)
		}
		got, r := readAll(t, b)
		assertRecords(t, got, records)
		if r.Dropped() != 0 {
			t.Fatalf("got %d dropped, want 0", r.Dropped())
		}
	})

	t.Run("Should keep returning io.EOF without changing dropped", func(t *testing.T) {
		b, _ := writeWithEnds(t, [][]byte{[]byte("a")})
		r := NewReader(bytes.NewReader(append(b, 0x01, 0x02, 0x03)))
		for {
			if _, err := r.Next(); err != nil {
				break
			}
		}
		dropped := r.Dropped()
		for range 3 {
			if _, err := r.Next(); !errors.Is(err, io.EOF) {
				t.Fatalf("got %v, want io.EOF", err)
			}
		}
		if r.Dropped() != dropped {
			t.Fatalf("dropped changed from %d to %d after repeated Next", dropped, r.Dropped())
		}
	})

	t.Run("Should skip zero filled padding until the next block", func(t *testing.T) {
		b := rawFragment(recordTypeFull, []byte("a"))
		b = append(b, make([]byte, blockSize-len(b))...)
		b = append(b, rawFragment(recordTypeFull, []byte("b"))...)
		got, r := readAll(t, b)
		assertRecords(t, got, [][]byte{[]byte("a"), []byte("b")})
		if r.Dropped() != 0 {
			t.Fatalf("got %d dropped, want 0", r.Dropped())
		}
	})
}

func TestReaderTruncation(t *testing.T) {
	records := variedRecords(7, 4)
	b, ends := writeWithEnds(t, records)

	check := func(t *testing.T, cut int) {
		t.Helper()
		complete := 0
		for complete < len(ends) && ends[complete] <= cut {
			complete++
		}
		lastEnd := 0
		if complete > 0 {
			lastEnd = ends[complete-1]
		}
		got, r := readAll(t, b[:cut])
		if len(got) != complete {
			t.Fatalf("cut %d: got %d records, want %d", cut, len(got), complete)
		}
		for i := range got {
			if !bytes.Equal(got[i], records[i]) {
				t.Fatalf("cut %d: record %d differs", cut, i)
			}
		}
		if r.Dropped() > cut-lastEnd {
			t.Fatalf("cut %d: dropped %d exceeds the %d bytes after the last complete record", cut, r.Dropped(), cut-lastEnd)
		}
		if cut == lastEnd && r.Dropped() != 0 {
			t.Fatalf("cut %d at a record boundary: got %d dropped, want 0", cut, r.Dropped())
		}
	}

	t.Run("Should recover exactly the complete records at every record boundary", func(t *testing.T) {
		check(t, 0)
		for _, e := range ends {
			check(t, e)
		}
	})

	t.Run("Should recover exactly the complete records around block boundaries", func(t *testing.T) {
		for blk := blockSize; blk < len(b); blk += blockSize {
			for d := -headerSize - 2; d <= headerSize+2; d++ {
				if c := blk + d; c >= 0 && c <= len(b) {
					check(t, c)
				}
			}
		}
	})

	t.Run("Should recover exactly the complete records at random cut points", func(t *testing.T) {
		rng := rand.New(rand.NewPCG(3, 3))
		for range 400 {
			check(t, rng.IntN(len(b)+1))
		}
	})
}

func TestReaderCorruption(t *testing.T) {
	small := func() ([][]byte, []byte, []int) {
		records := make([][]byte, 10)
		for i := range records {
			records[i] = payloadOf(100, byte(i))
		}
		b, ends := writeWithEnds(t, records)
		return records, b, ends
	}

	t.Run("Should stop before a record with a flipped payload byte", func(t *testing.T) {
		records, b, ends := small()
		b[ends[4]+headerSize+10] ^= 0xff
		got, r := readAll(t, b)
		assertRecords(t, got, records[:5])
		if r.Dropped() == 0 {
			t.Fatal("got 0 dropped, want > 0")
		}
	})

	t.Run("Should stop before a record with an invalid type byte", func(t *testing.T) {
		records, b, ends := small()
		b[ends[2]+6] = 9
		got, r := readAll(t, b)
		assertRecords(t, got, records[:3])
		if r.Dropped() == 0 {
			t.Fatal("got 0 dropped, want > 0")
		}
	})

	t.Run("Should stop without panicking when length points past the block", func(t *testing.T) {
		records, b, ends := small()
		b[ends[6]+4], b[ends[6]+5] = 0xff, 0xff
		got, _ := readAll(t, b)
		assertRecords(t, got, records[:7])
	})

	t.Run("Should stop on LAST without FIRST", func(t *testing.T) {
		got, r := readAll(t, rawFragment(recordTypeLast, []byte("x")))
		assertRecords(t, got, nil)
		if r.Dropped() == 0 {
			t.Fatal("got 0 dropped, want > 0")
		}
	})

	t.Run("Should stop on MIDDLE without FIRST", func(t *testing.T) {
		b := append(rawFragment(recordTypeFull, []byte("a")), rawFragment(recordTypeMiddle, []byte("b"))...)
		got, _ := readAll(t, b)
		assertRecords(t, got, [][]byte{[]byte("a")})
	})

	t.Run("Should stop on FULL inside a fragmented record", func(t *testing.T) {
		b := append(rawFragment(recordTypeFirst, []byte("a")), rawFragment(recordTypeFull, []byte("b"))...)
		got, r := readAll(t, b)
		assertRecords(t, got, nil)
		if r.Dropped() == 0 {
			t.Fatal("got 0 dropped, want > 0")
		}
	})

	t.Run("Should stop on FIRST inside a fragmented record", func(t *testing.T) {
		b := append(rawFragment(recordTypeFirst, []byte("a")), rawFragment(recordTypeFirst, []byte("b"))...)
		got, _ := readAll(t, b)
		assertRecords(t, got, nil)
	})
}

func TestReaderReadError(t *testing.T) {
	t.Run("Should return and keep returning the underlying read error", func(t *testing.T) {
		records := variedRecords(5, 4)
		b, _ := writeWithEnds(t, records)
		if len(b) <= blockSize {
			t.Fatal("setup: log must span more than one block")
		}
		r := NewReader(&failingReader{data: b, limit: blockSize})

		var err error
		for err == nil {
			_, err = r.Next()
		}
		if !errors.Is(err, errRead) {
			t.Fatalf("got %v, want %v", err, errRead)
		}
		if _, err := r.Next(); !errors.Is(err, errRead) {
			t.Fatalf("second Next: got %v, want %v", err, errRead)
		}
	})
}
