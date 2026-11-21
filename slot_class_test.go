package minigo

import (
	"context"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
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
		"AliasMethod":      true,
		"ShadowedParam":    true,
		// the CycB-shadowed embed cycle drains its depth budget and
		// settles unprovable — PP exists on neither decl.
		"CycShadow": false,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			if got := anyBoxed(t, name); got != want {
				t.Errorf("boxed=%v, want %v", got, want)
			}
		})
	}

	// a user type named `int` shadows the builtin for its whole package
	// — a declTyp spelled `int` there names the declared type and its
	// pointer methods, not the builtin.
	t.Run("ShadowedPredecl", func(t *testing.T) {
		pkg2, err := e.Package(ctx, "./testdata/slotshadow")
		if err != nil {
			t.Fatalf("Package(slotshadow): %v", err)
		}
		v, err := pkg2.MemberV("Use", e.materialize, nil)
		if err != nil {
			t.Fatalf("MemberV(Use): %v", err)
		}
		fn := v.(*runtime.Function)
		if err := fn.EnsureCompiled(); err != nil {
			t.Fatalf("EnsureCompiled(Use): %v", err)
		}
		if !slices.Contains(fn.Chunk.Boxed, true) {
			t.Errorf("Use: no boxed slot; the *int receiver cell must survive")
		}
	})
}

// TestPooledCellPublish pins the B=1 mark on publishable select refs:
// a method value or a field slice bound on a compile-unprovable
// member (generic receiver, cellUnknown) outlives its frame, so the
// promoted cell must come from the heap — not the frame's pooled
// block, which freeFrame clears.
func TestPooledCellPublish(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".")
	for name, want := range map[string]any{
		"Main":  int64(7),
		"Slice": int64(3),
	} {
		v, err := e.Run(ctx, "./testdata/cellpool", name)
		if err != nil {
			t.Fatalf("Run(%s): %v", name, err)
		}
		if diff := cmp.Diff(want, v); diff != "" {
			t.Errorf("Run(%s) mismatch (-want +got):\n%s", name, diff)
		}
	}
}
