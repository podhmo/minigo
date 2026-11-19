package stdlib

import (
	"fmt"
	"slices"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// yieldOK reports whether a yield call's result lets the producer go on.
func YieldOK(r runtime.Value) bool {
	goon, ok := r.(bool)
	return !ok || goon
}

// seqFn builds an intrinsic seq builtin over a native producer: the
// VM-facing Fn drives each with the script's yield func, and Go-side
// consumers read src directly (see nativeSeq).
func SeqFn(name string, src *runtime.SeqSource) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: "iter.Seq(" + name + ")", SyncCallbacks: true, Seq: src, Fn: func(vc runtime.VMCaller, ya []runtime.Value) (runtime.Value, error) {
		if len(ya) != 1 {
			return nil, fmt.Errorf("%s: Seq needs a yield func", name)
		}
		return runtime.NIL, src.Each(vc, ScriptYield(vc, ya[0]))
	}}
}

// scriptYield adapts a script yield func to a native producer's yield.
// The range-over-func loop body (DirectYield) runs straight from Go; any
// other callable goes through the VM with a fresh argument slice, and a
// panic in it propagates as a Go panic so a recover around the range
// sees it, like in Go.
func ScriptYield(vc runtime.VMCaller, yield runtime.Value) func(args []runtime.Value) bool {
	if c, ok := yield.(*runtime.BuiltinFunc); ok && c.DirectYield {
		return func(args []runtime.Value) bool {
			r, err := c.Fn(vc, args)
			if err != nil {
				panic(err)
			}
			return YieldOK(r)
		}
	}
	return func(args []runtime.Value) bool {
		return YieldOK(hostbridge.CallOrPanic(vc, yield, slices.Clone(args)))
	}
}

// sliceSource is the native source over pieces materialized up front
// (strings.SplitSeq and friends, maps over host values).
func SliceSource(pieces []runtime.Value) *runtime.SeqSource {
	return &runtime.SeqSource{
		Each: func(_ runtime.ScriptCaller, yield func([]runtime.Value) bool) error {
			buf := make([]runtime.Value, 1)
			for _, p := range pieces {
				buf[0] = p
				if !yield(buf) {
					return nil
				}
			}
			return nil
		},
		Collect: func(runtime.ScriptCaller) []runtime.Value { return slices.Clone(pieces) },
		// the pieces exist already, so a range loop may iterate the
		// slice itself rather than parking frames per yield.
		Materialized: pieces,
	}
}

// nilish reports whether a value carries no concrete content — bare nil,
// a typed nil, or a named wrapper around one.
func Nilish(v runtime.Value) bool {
	switch runtime.Unwrap(v).(type) {
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return true
	}
	return false
}

// seqOf builds an iter.Seq-shaped value: a callable the range-over-func
// loop drives through its yield builtin — the producer runs once and the
// loop body executes inside each yield call, so early exit just reports
// false back. Pieces are materialized up front; laziness is unobservable
// from a script.
func SeqOf(name string, pieces []runtime.Value) *runtime.BuiltinFunc {
	return SeqFn(name, SliceSource(pieces))
}

// strPieces boxes string pieces as script values for seqOf.
func StrPieces(ss []string) []runtime.Value {
	out := make([]runtime.Value, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// strArgs marshals trailing varargs (each already a script value) to strings.
func StrArgs(a []any) []string {
	out := make([]string, len(a))
	for i, v := range a {
		out[i] = Str(v)
	}
	return out
}

// runePred adapts a script callback into a rune predicate — strings'
// TrimFunc/IndexFunc family and unicode's Is-print predicates call it.
func RunePred(v runtime.ScriptCaller, fn runtime.Value) func(rune) bool {
	return func(r rune) bool {
		res := hostbridge.CallOrPanic(v, fn, []runtime.Value{int64(r)})
		b, _ := res.(bool)
		return b
	}
}

func RuneOf(v any) rune { return rune(IntOf(v)) }
