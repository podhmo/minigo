package minigo

import (
	"context"
	"slices"
	"testing"

	"github.com/podhmo/minigo/runtime"
)

// TestSlotClassification pins the per-function escape classification
// (P4-2a): every case asserts whether its chunk carries at least one
// boxed slot, per testdata/slotclass.
func TestSlotClassification(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".")
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/define", Name: "Convert"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			return runtime.NIL, nil
		})
	pkg, err := e.Package(ctx, "./testdata/slotclass")
	if err != nil {
		t.Fatalf("Package: %v", err)
	}

	anyBoxed := func(t *testing.T, name string) bool {
		t.Helper()
		v, err := pkg.MemberV(name, e.materialize, nil)
		if err != nil {
			t.Fatalf("MemberV(%s): %v", name, err)
		}
		fn, ok := v.(*runtime.Function)
		if !ok {
			t.Fatalf("%s is %T, want *runtime.Function", name, v)
		}
		if err := fn.EnsureCompiled(); err != nil {
			t.Fatalf("EnsureCompiled(%s): %v", name, err)
		}
		return slices.Contains(fn.Chunk.Boxed, true)
	}

	cases := map[string]bool{
		"Store":            false,
		"Addr":             true,
		"AddrField":        true,
		"AddrElem":         false,
		"FieldRead":        false,
		"ValueMethodCall":  false,
		"PtrMethodCall":    true,
		"FuncFieldCall":    false,
		"ValueMethodValue": false,
		"PtrMethodValue":   true,
		"IfaceCall":        false,
		"PromotedCall":     true,
		"MapElem":          false,
		"SliceElemMethod":  true,
		"Closure":          true,
		"InferPromoted":    true,
		"InferField":       false,
		"Unknown":          true,
		"PtrTyped":         false,
		"DeferPtr":         true,
		"Special":          true,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if got := anyBoxed(t, name); got != want {
				t.Errorf("boxed=%v, want %v", got, want)
			}
		})
	}
}
