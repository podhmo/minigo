package minigo

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// TestDisasmGolden pins bytecode.Disasm output for a representative
// function set (M0-8): closures, defer, range, a special form,
// generics, multi-value return, goto, variadic/spread, and a method.
// A compile change shows up as a golden diff. Regenerate with
// MINIGO_UPDATE_GOLDEN=1.
func TestDisasmGolden(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".")
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/define", Name: "Convert"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			return runtime.NIL, nil
		})
	pkg, err := e.Package(ctx, "./testdata/disasm")
	if err != nil {
		t.Fatalf("Package: %v", err)
	}

	chunkOf := func(t *testing.T, name string) *bytecode.Chunk {
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
		return fn.Chunk
	}

	nested := func(cst any) *bytecode.Chunk {
		if f, ok := cst.(*runtime.Function); ok {
			if err := f.EnsureCompiled(); err != nil {
				t.Fatalf("EnsureCompiled(nested %s): %v", f.Name, err)
			}
			return f.Chunk
		}
		return nil
	}

	names := []string{
		"Add", "Closure", "Deferred", "RangeSlice", "RangeMap",
		"MultiRet", "NamedRet", "Generic", "Goto", "Variadic",
		"Spread", "Special",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			got := bytecode.DisasmNested(chunkOf(t, name), nested)
			checkDisasmGolden(t, name, got)
		})
	}

	t.Run("Counter.Bump", func(t *testing.T) {
		v, err := pkg.MemberV("Counter", e.materialize, nil)
		if err != nil {
			t.Fatalf("MemberV(Counter): %v", err)
		}
		td, ok := v.(*runtime.TypeDef)
		if !ok {
			t.Fatalf("Counter is %T, want *runtime.TypeDef", v)
		}
		fn := td.Methods["Bump"]
		if fn == nil {
			t.Fatal("Counter.Bump not found")
		}
		if err := fn.EnsureCompiled(); err != nil {
			t.Fatalf("EnsureCompiled(Bump): %v", err)
		}
		checkDisasmGolden(t, "Counter.Bump", bytecode.DisasmNested(fn.Chunk, nested))
	})
}

func checkDisasmGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "disasm", name+".golden")
	if os.Getenv("MINIGO_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (regenerate with MINIGO_UPDATE_GOLDEN=1): %v", path, err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", path, diff)
	}
}
