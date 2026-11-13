package minigo_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// arrayRaceBlock is one iteration of testdata/arrayrace's work(), as the
// go toolchain prints it.
const arrayRaceBlock = `[2]main.pt 2 false [{9 1} {2 2}]|0 11 2main.Derived false [[1 0 0] [0 0 7] [0 0 0]] base1|6 3 [1 2 3]4[3 4] 1main.Pair[[3]int,[2]main.pt] {[1 2 3] [{1 1} {2 2}]}[[1 2] [3 4]]a2 1[6]int 6[[1 2] [2 4]][4]int [8]string true 8[3]int [3]string [2]main.pt 3 3 trueR B[{1 1} {2 2}]array 3 3[3][3]int221 3 2[100 2 3] [100 2 3]`

// TestArrayTypedefConcurrent runs array-typed script code on several
// goroutines at once. The array types' AST is shared between them and
// the interpreter folds each evaluated length (`[N]T`, `[...]T{}`) into
// the type on first use, so every iteration takes a fresh engine — a
// first use is a race window only once. The race detector (go test
// -race) is what fails when a fold writes into the shared AST; without
// it the test still checks that every goroutine computed the same,
// go-identical result.
func TestArrayTypedefConcurrent(t *testing.T) {
	want := strings.Repeat(arrayRaceBlock, 10)
	for i := 0; i < 6; i++ {
		e := newEngine(t)
		got := run(t, e, "./testdata/arrayrace", "Concurrent")
		if diff := cmp.Diff(want, got); diff != "" {
			t.Fatalf("iteration %d: Concurrent mismatch (-want +got):\n%s", i, diff)
		}
	}
}

// TestArrayTypedefSequential pins the single-goroutine reference the
// concurrent run is compared against, and the folded identity of
// differently spelled lengths.
func TestArrayTypedefSequential(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		fn   string
		want string
	}{
		{"Sequential", strings.Repeat(arrayRaceBlock, 10)},
		{"Identity", "true true true true [3]int [3]int [3]int [3]int [3]int"},
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/arrayrace", c.fn)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
		}
	}
}
