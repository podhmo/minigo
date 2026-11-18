package vm

import (
	"errors"
	"fmt"
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

	t.Run("instantiation binds key the site's cache", func(t *testing.T) {
		asks := 0
		v := newVM(nil, nil, &asks)
		ids := map[*runtime.TypeDef]runtime.TypeID{}
		v.H.TypeIDOf = func(td *runtime.TypeDef) runtime.TypeID {
			if _, ok := ids[td]; !ok {
				ids[td] = runtime.TypeID(fmt.Sprintf("t%d", len(ids)))
			}
			return ids[td]
		}
		intTd, strTd := runtime.BasicTypedef("int"), runtime.BasicTypedef("string")
		v.H.ResolveType = func(ctx *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
			asks++
			bt, _ := ctx.Binds["T"].(*runtime.TypeDef)
			return bt, nil
		}
		ch := chunk(siteFor(), siteFor())
		mkfn := func(arg runtime.Value) *runtime.Function {
			return &runtime.Function{Chunk: ch, Pkg: &runtime.Package{}, Binds: map[string]runtime.Value{"T": arg}}
		}
		if _, err := call(v, ch); err != nil { // no binds: hook sees nil Binds
			t.Fatalf("call: %v", err)
		}
		ri, err := v.Call(mkfn(intTd), nil)
		if err != nil {
			t.Fatalf("int call: %v", err)
		}
		rs, err := v.Call(mkfn(strTd), nil)
		if err != nil {
			t.Fatalf("string call: %v", err)
		}
		if ri != intTd || rs != strTd {
			t.Fatalf("binds leaked through the site cache: %v, %v", ri, rs)
		}
		if asks != 3 {
			t.Fatalf("hook asked %d times, want 3 (unbound, int, string)", asks)
		}
		// the int instantiation's answer replays without re-asking.
		if _, err := v.Call(mkfn(intTd), nil); err != nil {
			t.Fatalf("int replay: %v", err)
		}
		if asks != 3 {
			t.Fatalf("binds-keyed cache missed: %d asks", asks)
		}
	})

	t.Run("pinned context caches across instantiations", func(t *testing.T) {
		asks := 0
		td := runtime.BasicTypedef("int")
		v := newVM(td, nil, &asks)
		pinned := &runtime.TypeDef{Pkg: &runtime.Package{}}
		site := siteFor()
		site.Ctx = pinned
		ch := chunk(site, siteFor())
		for _, arg := range []runtime.Value{runtime.BasicTypedef("int"), runtime.BasicTypedef("string")} {
			_, err := v.Call(&runtime.Function{Chunk: ch, Pkg: pinned.Pkg, Binds: map[string]runtime.Value{"T": arg}}, nil)
			if err != nil {
				t.Fatalf("call: %v", err)
			}
		}
		if asks != 1 {
			t.Fatalf("pinned context re-asked: %d", asks)
		}
	})

	t.Run("deferred hook wins over ResolveType", func(t *testing.T) {
		asks := 0
		td := runtime.BasicTypedef("int")
		v := newVM(nil, errors.New("fallback must not run"), &asks)
		v.H.DeferredType = func(site *bytecode.CallSite, ctx *runtime.TypeDef) (*runtime.TypeDef, error) {
			asks++
			if _, isStar := site.Expr.(*ast.StarExpr); !isStar {
				t.Errorf("hook saw %T, want the original *ast.StarExpr", site.Expr)
			}
			return td, nil
		}
		site := siteFor()
		site.Expr = &ast.StarExpr{X: ast.NewIdent("int")}
		r, err := call(v, chunk(site, siteFor()))
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		if r != td || asks != 1 {
			t.Fatalf("got %v with %d asks", r, asks)
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
