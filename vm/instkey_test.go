package vm

import (
	"go/ast"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

// The instance cache keys on (decl, TypeIDs): two separately-minted
// typedefs of one type share the cached instance, distinct types and
// unidentified ones do not.
func TestInstCacheKey(t *testing.T) {
	ids := map[*runtime.TypeDef]runtime.TypeID{}
	mint := func(id runtime.TypeID) *runtime.TypeDef {
		td := &runtime.TypeDef{Kind: runtime.KindNamedBasic}
		ids[td] = id
		return td
	}
	v := &VM{H: Hooks{TypeIDOf: func(td *runtime.TypeDef) runtime.TypeID {
		return ids[td]
	}}}
	decl := &ast.FuncDecl{Name: ast.NewIdent("F")}
	fn := func(t *runtime.TypeDef) *runtime.Function {
		return &runtime.Function{Decl: decl, Binds: map[string]runtime.Value{"T": t}}
	}
	intKey := func(t *runtime.TypeDef) (runtime.InstCacheKey, bool) {
		return v.instFuncKey(fn(t), []runtime.Value{t})
	}

	k1, ok1 := intKey(mint("host:int"))
	k2, ok2 := intKey(mint("host:int"))
	k3, ok3 := intKey(mint("host:string"))
	_, ok4 := intKey(mint("?"))

	got := map[string]bool{
		"two mints of one type share": ok1 && ok2 && k1 == k2,
		"distinct types differ":       ok1 && ok3 && k1 != k3,
		"unknown type is uncached":    ok4,
		"non-typedef bind is uncached": func() bool {
			_, ok := v.instFuncKey(&runtime.Function{Decl: decl, Binds: map[string]runtime.Value{"T": runtime.Nil{}}}, nil)
			return ok
		}(),
	}
	want := map[string]bool{
		"two mints of one type share":  true,
		"distinct types differ":        true,
		"unknown type is uncached":     false,
		"non-typedef bind is uncached": false,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("instFuncKey (-want +got):\n%s", diff)
	}

	// inference keys join binds and argument types under the same decl
	arg := []runtime.Value{runtime.Nil{}}
	i1, iok1 := v.inferKey(fn(mint("host:int")), arg, []*runtime.TypeDef{mint("host:string")}, nil)
	i2, iok2 := v.inferKey(fn(mint("host:int")), arg, []*runtime.TypeDef{mint("host:string")}, nil)
	i3, iok3 := v.inferKey(fn(mint("host:int")), arg, []*runtime.TypeDef{mint("host:int")}, nil)
	gotI := map[string]bool{
		"same call spells the same key": iok1 && iok2 && i1 == i2,
		"another arg type differs":      iok1 && iok3 && i1 != i3,
	}
	wantI := map[string]bool{
		"same call spells the same key": true,
		"another arg type differs":      true,
	}
	if diff := cmp.Diff(wantI, gotI); diff != "" {
		t.Errorf("inferKey (-want +got):\n%s", diff)
	}
}
