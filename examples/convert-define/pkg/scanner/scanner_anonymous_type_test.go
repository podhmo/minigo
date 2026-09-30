package scanner

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTypeInfoFromExpr_AnonymousTypes(t *testing.T) {
	ctx := context.Background()
	s := newTestScanner(t, "example.com/test", "/tmp/test")
	pkgInfo := &PackageInfo{
		Name:       "main",
		ImportPath: "example.com/test",
	}

	tests := []struct {
		name              string
		source            string
		findNode          func(t *testing.T, f *ast.File) ast.Expr
		validateFieldType func(t *testing.T, ft *FieldType)
	}{
		{
			name: "anonymous interface",
			source: `
package main
type Request struct {
	Body interface {
		Read(p []byte) (n int, err error)
		Close() error
	}
}`,
			findNode: func(t *testing.T, f *ast.File) ast.Expr {
				typeSpec := f.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
				structType := typeSpec.Type.(*ast.StructType)
				return structType.Fields.List[0].Type
			},
			validateFieldType: func(t *testing.T, ft *FieldType) {
				if ft.Definition == nil {
					t.Fatal("FieldType.Definition should not be nil for anonymous interface")
				}
				ti := ft.Definition
				if ti.Kind != InterfaceKind {
					t.Errorf("expected kind InterfaceKind, got %v", ti.Kind)
				}
				if ti.Interface == nil {
					t.Fatal("TypeInfo.Interface should not be nil")
				}
				if len(ti.Interface.Methods) != 2 {
					t.Fatalf("expected 2 methods, got %d", len(ti.Interface.Methods))
				}
				if ti.Interface.Methods[0].Name != "Read" {
					t.Errorf("expected method 'Read', got '%s'", ti.Interface.Methods[0].Name)
				}
				if len(ti.Interface.Methods[0].Parameters) != 1 {
					t.Errorf("expected 1 parameter for Read, got %d", len(ti.Interface.Methods[0].Parameters))
				}
				if len(ti.Interface.Methods[0].Results) != 2 {
					t.Errorf("expected 2 results for Read, got %d", len(ti.Interface.Methods[0].Results))
				}
				if ti.Interface.Methods[1].Name != "Close" {
					t.Errorf("expected method 'Close', got '%s'", ti.Interface.Methods[1].Name)
				}
			},
		},
		{
			name: "anonymous struct",
			source: `
package main
type Response struct {
	Data struct {
		ID   int
		Name string
	}
}`,
			findNode: func(t *testing.T, f *ast.File) ast.Expr {
				typeSpec := f.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
				structType := typeSpec.Type.(*ast.StructType)
				return structType.Fields.List[0].Type
			},
			validateFieldType: func(t *testing.T, ft *FieldType) {
				if ft.Definition == nil {
					t.Fatal("FieldType.Definition should not be nil for anonymous struct")
				}
				ti := ft.Definition
				if ti.Kind != StructKind {
					t.Errorf("expected kind StructKind, got %v", ti.Kind)
				}
				if ti.Struct == nil {
					t.Fatal("TypeInfo.Struct should not be nil")
				}
				if len(ti.Struct.Fields) != 2 {
					t.Fatalf("expected 2 fields, got %d", len(ti.Struct.Fields))
				}
				expectedFields := map[string]string{"ID": "int", "Name": "string"}
				actualFields := make(map[string]string)
				for _, f := range ti.Struct.Fields {
					actualFields[f.Name] = f.Type.Name
				}
				if diff := cmp.Diff(expectedFields, actualFields); diff != "" {
					t.Errorf("struct fields mismatch (-want +got):\n%s", diff)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), "src.go", tt.source, 0)
			if err != nil {
				t.Fatalf("failed to parse source: %v", err)
			}

			node := tt.findNode(t, f)
			fieldType := s.TypeInfoFromExpr(ctx, node, nil, pkgInfo, nil)

			if fieldType == nil {
				t.Fatal("TypeInfoFromExpr returned nil")
			}

			tt.validateFieldType(t, fieldType)
		})
	}
}
