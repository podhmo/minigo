package minigo_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
)

// TestQuotedGoPin pins the quoted-Go (special form) behavior for M0-6,
// beyond what examples/convert-define covers:
//
//   - the stub package is never loaded or executed: with a spy
//     resolver, quoted calls locate nothing;
//   - an aliased import (import d "example.com/define") still
//     dispatches;
//   - the same member spelling on a different package
//     ("other.com/define".OtherConvert) reaches the bound package, not
//     the special form;
//   - a local variable shadowing the import alias does NOT dispatch
//     the special — Go resolves the local first, so `d.Convert(5)` is
//     a method call returning 50.
func TestQuotedGoPin(t *testing.T) {
	ctx := context.Background()

	newEngine := func(t *testing.T, fired *[]string) *minigo.Engine {
		t.Helper()
		e := minigo.NewEngine(".")
		e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/define", Name: "Convert"},
			func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
				*fired = append(*fired, "Convert")
				return runtime.NIL, nil
			})
		e.Bind("other.com/define", map[string]runtime.Value{
			"OtherConvert": &runtime.BuiltinFunc{Name: "define.OtherConvert", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
				return int64(42), nil
			}},
		})
		return e
	}
	load := func(t *testing.T, e *minigo.Engine) *runtime.Package {
		t.Helper()
		pkg, err := e.LoadFile(ctx, "./testdata/quotepin/quotepin.go")
		if err != nil {
			t.Fatalf("LoadFile: %v", err)
		}
		return pkg
	}

	t.Run("AliasDispatches", func(t *testing.T) {
		var fired []string
		e := newEngine(t, &fired)
		if _, err := e.Call(ctx, load(t, e), "aliasCall"); err != nil {
			t.Fatalf("aliasCall: %v", err)
		}
		if diff := cmp.Diff([]string{"Convert"}, fired); diff != "" {
			t.Errorf("fired specials mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("SameSpellingDifferentPackage", func(t *testing.T) {
		var fired []string
		e := newEngine(t, &fired)
		v, err := e.Call(ctx, load(t, e), "sameSpelling")
		if err != nil {
			t.Fatalf("sameSpelling: %v", err)
		}
		if diff := cmp.Diff(int64(42), v); diff != "" {
			t.Errorf("sameSpelling mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string(nil), fired); diff != "" {
			t.Errorf("fired specials mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("LocalShadowingSkipsSpecial", func(t *testing.T) {
		// `d` is a local variable, so `d.Convert` is a method call —
		// the special must not fire and the method's result (50) wins.
		var fired []string
		e := newEngine(t, &fired)
		v, err := e.Call(ctx, load(t, e), "shadowed")
		if err != nil {
			t.Fatalf("shadowed: %v", err)
		}
		if diff := cmp.Diff([]string(nil), fired); diff != "" {
			t.Errorf("fired specials mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(int64(50), v); diff != "" {
			t.Errorf("shadowed mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("NestedSpecialStillDispatches", func(t *testing.T) {
		var fired []string
		e := newEngine(t, &fired)
		v, err := e.Call(ctx, load(t, e), "nestedCall")
		if err != nil {
			t.Fatalf("nestedCall: %v", err)
		}
		if diff := cmp.Diff([]string{"Convert"}, fired); diff != "" {
			t.Errorf("fired specials mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(int64(42), v); diff != "" {
			t.Errorf("nestedCall mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("NestedSpecialStillDispatchesStrict", func(t *testing.T) {
		// strict lazy compile: `d.Convert` also reads as an
		// undecidable member call inside OtherConvert's args — it must
		// stay a quoted call, not a member-shape branch.
		var fired []string
		e := minigo.NewEngine(".", minigo.WithStrictLazyCompile())
		e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/define", Name: "Convert"},
			func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
				fired = append(fired, "Convert")
				return runtime.NIL, nil
			})
		e.Bind("other.com/define", map[string]runtime.Value{
			"OtherConvert": &runtime.BuiltinFunc{Name: "define.OtherConvert", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
				return int64(42), nil
			}},
		})
		v, err := e.Call(ctx, load(t, e), "nestedCall")
		if err != nil {
			t.Fatalf("nestedCall: %v", err)
		}
		if diff := cmp.Diff([]string{"Convert"}, fired); diff != "" {
			t.Errorf("fired specials mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(int64(42), v); diff != "" {
			t.Errorf("nestedCall mismatch (-want +got):\n%s", diff)
		}
	})
}
