package index_test

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/index"
)

func TestValueSpecs(t *testing.T) {
	type result struct {
		Index  int
		Names  string
		Type   string
		Values string
	}
	for _, tt := range []struct {
		name string
		src  string
		want []result
	}{
		{
			name: "const repetition and type reset",
			src: `const (
				A, B T = iota, iota * 10
				C, D
				E, F
				G = 7
				H
				I U = 8
				J
			)`,
			want: []result{
				{0, "A,B", "T", "iota,iota * 10"},
				{1, "C,D", "T", "iota,iota * 10"},
				{2, "E,F", "T", "iota,iota * 10"},
				{3, "G", "", "7"},
				{4, "H", "", "7"},
				{5, "I", "U", "8"},
				{6, "J", "U", "8"},
			},
		},
		{
			name: "var specs do not inherit",
			src:  `var (A T = 1; B U; C = 2; D V)`,
			want: []result{
				{0, "A", "T", "1"},
				{1, "B", "U", ""},
				{2, "C", "", "2"},
				{3, "D", "V", ""},
			},
		},
		{
			name: "each declaration starts fresh",
			src:  `const A T = 1; const (B; C = 2; D)`,
			want: []result{
				{0, "A", "T", "1"},
				{0, "B", "", ""},
				{1, "C", "", "2"},
				{2, "D", "", "2"},
			},
		},
		{name: "type declaration", src: `type T int`},
		{name: "import declaration", src: `import "fmt"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "specs.go", "package p\n"+tt.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			printNode := func(node any) string {
				t.Helper()
				if node == nil {
					return ""
				}
				var b bytes.Buffer
				if err := format.Node(&b, fset, node); err != nil {
					t.Fatal(err)
				}
				return b.String()
			}
			before := printNode(f)
			var got []result
			for _, decl := range f.Decls {
				gd := decl.(*ast.GenDecl)
				for i, effective := range index.ValueSpecs(gd) {
					if effective.Spec != gd.Specs[i] {
						t.Fatal("iterator replaced the original spec")
					}
					r := result{Index: i, Type: printNode(effective.Type)}
					for j, name := range effective.Spec.Names {
						if j > 0 {
							r.Names += ","
						}
						r.Names += name.Name
					}
					for j, value := range effective.Values {
						if j > 0 {
							r.Values += ","
						}
						r.Values += printNode(value)
					}
					got = append(got, r)
				}
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("effective specs (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(before, printNode(f)); diff != "" {
				t.Errorf("AST changed (-before +after):\n%s", diff)
			}
		})
	}
}
