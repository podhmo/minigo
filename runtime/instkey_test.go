package runtime

import (
	"go/ast"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInstKey(t *testing.T) {
	decl := &ast.FuncDecl{Name: ast.NewIdent("f")}
	key := func(binds map[string]Value) string {
		t.Helper()
		k, ok := InstKey(decl, nil, "f", binds)
		if !ok {
			t.Fatalf("InstKey(%v) not ok", binds)
		}
		return k
	}
	named := &TypeDef{Name: "N", Kind: KindNamedBasic, Anon: ast.NewIdent("string")}
	other := &TypeDef{Name: "N", Kind: KindNamedBasic, Anon: ast.NewIdent("string")}
	fresh := func() *TypeDef { return &TypeDef{Name: "string", Kind: KindNamedBasic} }
	spelled := fresh()
	spelled.OuterSpell = []Value{named}

	same := func(a, b map[string]Value) bool { return key(a) == key(b) }
	copyOf := func(td *TypeDef) *TypeDef { c := *td; return &c }
	respelled := copyOf(named)
	respelled.OuterSpell = []Value{fresh()}
	got := map[string]bool{
		"copies of one type share":   same(map[string]Value{"T": named}, map[string]Value{"T": copyOf(named)}),
		"display context separates":  same(map[string]Value{"T": named}, map[string]Value{"T": respelled}),
		"fresh basics share":         same(map[string]Value{"T": fresh()}, map[string]Value{"T": fresh()}),
		"outer spelling ignored":     same(map[string]Value{"T": spelled}, map[string]Value{"T": BasicTypedef("string")}),
		"basic names differ":         same(map[string]Value{"T": fresh()}, map[string]Value{"T": BasicTypedef("int")}),
		"named types by identity":    same(map[string]Value{"T": named}, map[string]Value{"T": other}),
		"same named type shares":     same(map[string]Value{"T": named}, map[string]Value{"T": named}),
		"bind order is irrelevant":   same(map[string]Value{"K": named, "V": fresh()}, map[string]Value{"V": fresh(), "K": named}),
		"local basic is not a basic": key(map[string]Value{"T": &TypeDef{Name: "string", Kind: KindNamedBasic, Local: true}}) == key(map[string]Value{"T": fresh()}),
	}
	want := map[string]bool{
		"copies of one type share":   true,
		"display context separates":  false,
		"fresh basics share":         true,
		"outer spelling ignored":     true,
		"basic names differ":         false,
		"named types by identity":    false,
		"same named type shares":     true,
		"bind order is irrelevant":   true,
		"local basic is not a basic": false,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("InstKey sharing (-want +got):\n%s", diff)
	}
	if _, ok := InstKey(decl, nil, "f", map[string]Value{"T": int64(1)}); ok {
		t.Errorf("InstKey with a non-typedef bind: ok, want not ok")
	}
}
