package runtime

import (
	"go/ast"
	"go/parser"
	"testing"
)

func typeExpr(t *testing.T, s string) ast.Expr {
	t.Helper()
	e, err := parser.ParseExpr(s)
	if err != nil {
		t.Fatalf("ParseExpr(%q): %v", s, err)
	}
	return e
}

func TestTypIdentical(t *testing.T) {
	pkg := &Package{Path: "example.com/p"}
	other := &Package{Path: "example.com/q"}
	td := func(name string) *TypeDef { return &TypeDef{Name: name, Pkg: pkg, Kind: KindNamedBasic} }
	anon := func(s string) *TypeDef {
		return &TypeDef{Kind: KindSlice, Anon: typeExpr(t, s)}
	}

	tests := []struct {
		name string
		a, b *TypeDef
		want bool
	}{
		{"same object", td("T"), td("T"), true},
		{"named same name+pkg", td("T"), &TypeDef{Name: "T", Pkg: pkg, Kind: KindNamedBasic}, true},
		{"named different name", td("T"), td("U"), false},
		{"named different pkg", td("T"), &TypeDef{Name: "T", Pkg: other, Kind: KindNamedBasic}, false},
		{"alias folds byte", &TypeDef{Name: "byte", Kind: KindNamedBasic}, &TypeDef{Name: "uint8", Kind: KindNamedBasic}, true},
		{"nil", nil, td("T"), false},
		{"anon []int vs []int", anon("[]int"), anon("[]int"), true},
		{"anon []byte vs []uint8", anon("[]byte"), anon("[]uint8"), true},
		{"anon []int vs []string", anon("[]int"), anon("[]string"), false},
		{"anon []int vs [3]int", anon("[]int"), anon("[3]int"), false},
		{"anon struct field types differ", anon("struct{X int}"), anon("struct{X any}"), false},
		{"anon struct field names differ", anon("struct{X int}"), anon("struct{Y int}"), false},
		{"anon struct same shape", anon("struct{X int}"), anon("struct{X int}"), true},
		{"anon vs named", anon("[]int"), td("T"), false},
	}
	for _, tt := range tests {
		if tt.name == "same object" {
			if !TypIdentical(tt.a, tt.a) {
				t.Errorf("same object: want true")
			}
			continue
		}
		if got := TypIdentical(tt.a, tt.b); got != tt.want {
			t.Errorf("%s: TypIdentical = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTypIdenticalStrict(t *testing.T) {
	pkg := &Package{Path: "example.com/p"}
	anon := func(s string) *TypeDef {
		return &TypeDef{Kind: KindStruct, Anon: typeExpr(t, s)}
	}

	// two `type T` decls are distinct types even in the same package —
	// unlike TypIdentical, strict identity compares the decl object.
	a := &TypeDef{Name: "T", Pkg: pkg, Kind: KindNamedBasic}
	b := &TypeDef{Name: "T", Pkg: pkg, Kind: KindNamedBasic}
	if TypIdenticalStrict(a, b) {
		t.Error("re-declared named typedefs must not compare strictly identical")
	}
	if !TypIdenticalStrict(a, a) {
		t.Error("same decl object must compare strictly identical")
	}
	// anonymous typedefs compare by underlying spelling.
	if !TypIdenticalStrict(anon("struct{X int}"), anon("struct{X int}")) {
		t.Error("same-shape anonymous structs should be strictly identical")
	}
	if TypIdenticalStrict(anon("struct{X int}"), anon("struct{X string}")) {
		t.Error("different field types must not be strictly identical")
	}
}

func TestTypSpellingQualifiesPackage(t *testing.T) {
	p := &Package{Path: "example.com/p"}
	e := typeExpr(t, "[]Foo")
	got := TypSpelling(e, &TypeDef{Pkg: p, Kind: KindSlice})
	if got != "[]example.com/p.Foo" {
		t.Fatalf("TypSpelling = %q", got)
	}
	// a typedef without a package context spells unqualified.
	if got := TypSpelling(e, nil); got != "[]Foo" {
		t.Fatalf("nil ctx TypSpelling = %q", got)
	}
}
