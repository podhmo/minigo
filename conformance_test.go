package minigo_test

// Conformance harness: the same Go source is run under minigo (the stack
// VM) and the entry-point results must match the expected values. Cases
// live in testdata/conformance/main.go.
//
// History: in podhmo/go-scan this test ran each case under both the v1
// tree-walking interpreter (go-scan/minigo) and minigo2 (the VM) and
// diffed the results. The v1 engine was not carried over to this
// repository, so the expected column below freezes v1's outputs captured
// at migration time (go-scan main @ 87cffe1). The VM must still produce
// the same values.
//
// Documented divergences were skipped upstream — e.g. single-threaded
// go/chan/select approximation and host-intrinsic packages (v1 had none).
import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

// norm2 renders a minigo runtime value into the Go shape v1's Result.As
// produces, so both engines compare on equal footing.
func norm2(v runtime.Value) any {
	switch x := v.(type) {
	case *runtime.Cell:
		return norm2(x.Elem)
	case *runtime.Tuple:
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			out[i] = norm2(e)
		}
		return out
	case *runtime.Slice:
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			out[i] = norm2(e)
		}
		return out
	case runtime.Nil:
		return nil
	default:
		return v // int64, string, bool, float64 pass through
	}
}

func TestConformance(t *testing.T) {
	e := newEngine(t)

	cases := []struct {
		fn    string
		want  any // v1 output, frozen at migration
		args  []runtime.Value
		v1arg bool // v1 cannot take args: skip arg cases there
	}{
		// scalar results — no args (v1 Run has no arg passing)
		{fn: "FibNoArg", want: int64(55)},   // fib(10) = 55, computed inside
		{fn: "LoopSum", want: int64(45)},    // 45
		{fn: "RangeSlice", want: int64(6)},  // 6
		{fn: "RangeMap", want: int64(3)},    // 3 (v1 map iteration)
		{fn: "Closure", want: int64(11)},    // 11
		{fn: "SwitchVal1", want: int64(10)}, // 10 — wrappers since v1 can't pass args
		{fn: "SwitchVal2", want: int64(20)}, // 20
		{fn: "UseMulti", want: int64(34)},   // 34
		{fn: "StrCat", want: "xxx"},         // "xxx"
	}

	for _, c := range cases {
		got2 := norm2(run(t, e, "./testdata/conformance", c.fn, c.args...))
		if diff := cmp.Diff(c.want, got2); diff != "" {
			t.Errorf("%s: minigo vs v1-frozen mismatch (-v1 +minigo):\n%s", c.fn, diff)
		}
	}
}

// TestConformanceV1Divergence documents where v1 and minigo legitimately
// disagreed: v1's Result.As could not unmarshal a defer-mutated named
// result (it came back as an opaque RETURN_VALUE object) — the VM handles
// it correctly, so the harness only checks that the VM produces the
// Go-semantics value.
func TestConformanceV1Divergence(t *testing.T) {
	e := newEngine(t)
	// defer mutates the named result before return: 1 + 5 = 6
	if got := run(t, e, "./testdata/conformance", "DeferRun"); got != int64(6) {
		t.Fatalf("DeferRun: got %v, want 6", got)
	}
}
