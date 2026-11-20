// SPDX-License-Identifier: MIT

package xxh3

import (
	"fmt"
	"testing"
)

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*7 + i/251) % 251)
	}
	return b
}

var vectors = []struct {
	n    int
	want string
}{
	{0, "2d06800538d394c2"},
	{1, "c44bdff4074eecdb"},
	{2, "9093381c8763d62e"},
	{3, "c3489259e968ad9e"},
	{4, "d3d60c1519014e89"},
	{5, "559935c0f3f7327f"},
	{7, "0ffc4827844c63a6"},
	{8, "b88dee77f6bf6980"},
	{9, "03688dcad730d826"},
	{12, "305c5bae3681d0e0"},
	{16, "9da23836adf2be1e"},
	{17, "f34c3c9cf5a112d1"},
	{31, "1c67766271b95c64"},
	{32, "99cb9ad0f1a11fbe"},
	{33, "c077b45492d29cde"},
	{64, "faecd52effbb854f"},
	{65, "fd2b8a4f15d954d0"},
	{96, "8f8a0c940d32e958"},
	{97, "92b3a831febf71ac"},
	{128, "4c659b745f435148"},
	{129, "b85222b83902b6e5"},
	{150, "d1d52879d6f328e5"},
	{192, "2fb08234ce68b6d4"},
	{240, "d6dfbf67b0675b54"},
	{241, "c614c8c3575348c1"},
	{255, "af25cce47724fcfc"},
	{256, "031c99a649cf3c5e"},
	{512, "eafe02afa18954b0"},
	{1023, "ad4de5a1e560d795"},
	{1024, "1e28332b5ac3dfa0"},
	{1025, "634288c9f20aca18"},
	{2047, "1666551e33043fb8"},
	{2048, "d7cda26503199c15"},
	{2049, "5849575d24043d45"},
	{3000, "06b9455aaea89abf"},
}

func TestSum64(t *testing.T) {
	t.Run("Should match the reference implementation for every size class", func(t *testing.T) {
		data := pattern(3000)
		for _, v := range vectors {
			if got := fmt.Sprintf("%016x", Sum64(data[:v.n])); got != v.want {
				t.Errorf("length %d: got %s, want %s", v.n, got, v.want)
			}
		}
	})
	t.Run("Should hash the empty string to the known value", func(t *testing.T) {
		if got := fmt.Sprintf("%016x", Sum64(nil)); got != "2d06800538d394c2" {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("Should not panic or change the input for any length", func(t *testing.T) {
		data := pattern(1200)
		copyOf := append([]byte(nil), data...)
		for n := range len(data) + 1 {
			_ = Sum64(data[:n])
		}
		for i := range data {
			if data[i] != copyOf[i] {
				t.Fatal("the input changed")
			}
		}
	})
}

func FuzzSum64(f *testing.F) {
	f.Add([]byte("hello"))
	f.Add(pattern(300))
	f.Fuzz(func(t *testing.T, b []byte) {
		if Sum64(b) != Sum64(append([]byte(nil), b...)) {
			t.Fatal("the hash depends on the memory of the input")
		}
	})
}
