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
//   - KNOWN DIVERGENCE: a local variable shadowing the import alias
//     still dispatches the special — Go resolves the local first, so
//     `d.Convert(5)` should be a method call returning 50, but today
//     the importRef lookup ignores local scope and the special fires.
//     Pinned as the current (wrong) behavior; recorded in TODO.md.
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

	t.Run("LocalShadowingStillDispatches", func(t *testing.T) {
		// current behavior: the special fires even though `d` is a
		// local variable — Go semantics would call the method and
		// return 50.
		var fired []string
		e := newEngine(t, &fired)
		v, err := e.Call(ctx, load(t, e), "shadowed")
		if err != nil {
			t.Fatalf("shadowed: %v", err)
		}
		if diff := cmp.Diff([]string{"Convert"}, fired); diff != "" {
			t.Errorf("fired specials mismatch (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(int64(0), v); diff != "" {
			t.Errorf("shadowed mismatch (-want +got):\n%s", diff)
		}
	})
}
