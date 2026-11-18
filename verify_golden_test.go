package minigo

import (
	"context"
	"testing"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// verifyFn compiles a function and verifies its chunk plus every
// nested function reachable through its constants (closures land
// there as *runtime.Function protos).
func verifyFn(t *testing.T, fn *runtime.Function) {
	t.Helper()
	if err := fn.EnsureCompiled(); err != nil {
		t.Fatalf("EnsureCompiled(%s): %v", fn.Name, err)
	}
	seen := map[*bytecode.Chunk]bool{}
	var walk func(ch *bytecode.Chunk, label string)
	walk = func(ch *bytecode.Chunk, label string) {
		if ch == nil || seen[ch] {
			return
		}
		seen[ch] = true
		if _, err := bytecode.Verify(ch); err != nil {
			t.Fatalf("Verify(%s):\n%s\n%v", label, bytecode.Disasm(ch), err)
		}
		if _, err := bytecode.VerifyKinds(ch); err != nil {
			t.Fatalf("VerifyKinds(%s):\n%s\n%v", label, bytecode.Disasm(ch), err)
		}
		for _, cst := range ch.Consts {
			if f, ok := cst.(*runtime.Function); ok {
				if err := f.EnsureCompiled(); err != nil {
					t.Fatalf("EnsureCompiled(nested %s): %v", f.Name, err)
				}
				walk(f.Chunk, f.Name)
			}
		}
	}
	walk(fn.Chunk, fn.Name)
}

// TestVerifyCompiled runs the verifier over every chunk the compiler
// emits for the disasm corpus — the positive side of P4-1a: real
// control flow (loops, select, defer, generics, closures) must verify.
func TestVerifyCompiled(t *testing.T) {
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
	for _, name := range []string{
		"Add", "Closure", "Deferred", "RangeSlice", "RangeMap",
		"MultiRet", "NamedRet", "Generic", "Goto", "Variadic",
		"Spread", "Special",
	} {
		v, err := pkg.MemberV(name, e.materialize, nil)
		if err != nil {
			t.Fatalf("MemberV(%s): %v", name, err)
		}
		fn, ok := v.(*runtime.Function)
		if !ok {
			t.Fatalf("%s is %T, want *runtime.Function", name, v)
		}
		verifyFn(t, fn)
	}
	// a method chunk too — it has a receiver cell and its own frame.
	v, err := pkg.MemberV("Counter", e.materialize, nil)
	if err != nil {
		t.Fatalf("MemberV(Counter): %v", err)
	}
	td, ok := v.(*runtime.TypeDef)
	if !ok {
		t.Fatalf("Counter is %T, want *runtime.TypeDef", v)
	}
	verifyFn(t, td.Methods["Bump"])
}

// TestWithBytecodeVerify pins the debug entry point (P4-1b): an
// engine built with WithBytecodeVerify runs Verify + VerifyKinds on
// every chunk it compiles — a healthy corpus still compiles and runs.
func TestWithBytecodeVerify(t *testing.T) {
	ctx := context.Background()
	e := NewEngine(".", WithBytecodeVerify())
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/define", Name: "Convert"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			return runtime.NIL, nil
		})
	pkg, err := e.Package(ctx, "./testdata/disasm")
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	for _, name := range []string{
		"Add", "Closure", "Deferred", "RangeSlice", "RangeMap",
		"MultiRet", "NamedRet", "Generic", "Goto", "Variadic",
		"Spread", "Special",
	} {
		v, err := pkg.MemberV(name, e.materialize, nil)
		if err != nil {
			t.Fatalf("MemberV(%s): %v", name, err)
		}
		fn, ok := v.(*runtime.Function)
		if !ok {
			t.Fatalf("%s is %T, want *runtime.Function", name, v)
		}
		if err := fn.EnsureCompiled(); err != nil {
			t.Fatalf("EnsureCompiled(%s) under WithBytecodeVerify: %v", name, err)
		}
	}
}
