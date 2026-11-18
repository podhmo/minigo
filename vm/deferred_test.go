package vm

import (
	"errors"
	"go/ast"
	"testing"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// M4-7a: OpDeferredTyp answers a deferred fact on first execution under
// the engine's ResolveType hook, traps on Missing/Failed, and never asks
// for a site whose instruction did not run.
func TestDeferredTyp(t *testing.T) {
	newVM := func(td *runtime.TypeDef, rerr error, asks *int) *VM {
		v := &VM{}
		v.H.ResolveType = func(ctx *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
			*asks++
			return td, rerr
		}
		return v
	}
	siteFor := func() *bytecode.CallSite {
		return &bytecode.CallSite{Expr: &ast.Ident{Name: "T"}}
	}
	// [push false; jump over site1's instruction; resolve site0; return]
	chunk := func(sites ...*bytecode.CallSite) *bytecode.Chunk {
		return &bytecode.Chunk{
			Code: []bytecode.Instruction{
				{Op: bytecode.OpConst, A: 0},
				{Op: bytecode.OpJumpFalse, A: 3},
				{Op: bytecode.OpDeferredTyp, A: 1}, // skipped — never asked
				{Op: bytecode.OpDeferredTyp, A: 0},
				{Op: bytecode.OpReturn, A: 1},
			},
			Consts:    []any{false},
			CallSites: sites,
		}
	}
	call := func(v *VM, ch *bytecode.Chunk) (runtime.Value, error) {
		return v.Call(&runtime.Function{Chunk: ch, Pkg: &runtime.Package{}}, nil)
	}

	t.Run("resolves once and caches", func(t *testing.T) {
		asks := 0
		td := runtime.BasicTypedef("int")
		v := newVM(td, nil, &asks)
		ch := chunk(siteFor(), siteFor())
		r, err := call(v, ch)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		if r != td {
			t.Fatalf("resolved %v, want the hook's typedef", r)
		}
		if asks != 1 {
			t.Fatalf("hook asked %d times, want 1 (site0 only)", asks)
		}
		// a second call reuses the site's cached answer.
		if _, err := call(v, ch); err != nil {
			t.Fatalf("second call: %v", err)
		}
		if asks != 1 {
			t.Fatalf("cache miss: hook asked %d times", asks)
		}
	})

	t.Run("missing traps at the instruction", func(t *testing.T) {
		asks := 0
		v := newVM(nil, errors.New("index has no such type"), &asks)
		_, err := call(v, chunk(siteFor(), siteFor()))
		tr, ok := err.(*runtime.Trap)
		if !ok {
			t.Fatalf("got %T %v, want *runtime.Trap", err, err)
		}
		if tr.Reason != "index has no such type" {
			t.Fatalf("trap reason %q", tr.Reason)
		}
	})

	t.Run("failure traps with the cause", func(t *testing.T) {
		asks := 0
		v := newVM(nil, errors.New("disk read blew up"), &asks)
		_, err := call(v, chunk(siteFor(), siteFor()))
		if _, ok := err.(*runtime.Trap); !ok {
			t.Fatalf("got %T %v, want *runtime.Trap", err, err)
		}
	})

	t.Run("no hook traps", func(t *testing.T) {
		v := &VM{}
		_, err := call(v, chunk(siteFor(), siteFor()))
		if _, ok := err.(*runtime.Trap); !ok {
			t.Fatalf("got %T %v, want *runtime.Trap", err, err)
		}
	})
}
