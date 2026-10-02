package generator

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// PackageIdents lists the top-level identifiers declared by the .go
// files in dir — the package a generated file joins. It is syntax only
// (no type checking, no imports followed), so it works while the
// package does not compile; a file that does not parse contributes
// whatever declarations the parser recovered. Over-reserving is
// harmless (a temporary just gets a numeric suffix), so every file is
// read regardless of build tags or a stale generated output.
func PackageIdents(ctx context.Context, dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.DebugContext(ctx, "cannot list package dir for identifier reservation", "dir", dir, "error", err)
		return nil
	}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") {
			continue
		}
		f, _ := parser.ParseFile(fset, filepath.Join(dir, ent.Name()), nil, parser.SkipObjectResolution)
		if f == nil {
			continue
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					seen[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						seen[s.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range s.Names {
							seen[n.Name] = true
						}
					}
				}
			}
		}
	}
	idents := make([]string, 0, len(seen))
	for id := range seen {
		if id != "_" {
			idents = append(idents, id)
		}
	}
	slices.Sort(idents)
	return idents
}
