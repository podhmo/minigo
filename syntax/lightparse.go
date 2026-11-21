package syntax

// lightparse.go — header-only parse stages for scans that need no full
// AST (repo enumeration, import-graph walks). Each returns a narrow
// result, so a truncated *ast.File can never flow downstream into a
// full-AST consumer (the go-scan `ScanPackageFromFilePathImports` shape).

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
)

// ParsePackageClause reads only the `package` clause of src (or filename
// when src is nil) — go/parser's PackageClauseOnly stage — and returns
// the declared name. The result is just the name: no truncated *ast.File
// can reach a full-AST consumer by mistake.
func ParsePackageClause(filename string, src []byte) (string, error) {
	filename = filepath.ToSlash(filename)
	var srcAny any
	if src != nil {
		srcAny = src
	}
	f, err := parser.ParseFile(token.NewFileSet(), filename, srcAny, parser.PackageClauseOnly)
	if err != nil {
		return "", err
	}
	if f.Name == nil {
		return "", fmt.Errorf("%s: no package clause", filename)
	}
	return f.Name.Name, nil
}

// ParseImports reads the header of src (or filename when src is nil) —
// go/parser's ImportsOnly stage — and returns the package clause name
// and the file's [Import] table. Nothing else of the AST survives, so a
// header-only scan cannot leak a truncated *ast.File downstream. Syntax
// errors past the header are never scanned (err stays nil); a malformed
// header still returns the recovered imports with the error attached,
// so the caller decides whether a partial table is usable.
func ParseImports(fset *token.FileSet, filename string, src []byte) (string, []*Import, error) {
	filename = filepath.ToSlash(filename)
	var srcAny any
	if src != nil {
		srcAny = src
	}
	f, err := parser.ParseFile(fset, filename, srcAny, parser.ImportsOnly)
	if err != nil && f == nil {
		return "", nil, err
	}
	imports, ierr := importTable(f, filename)
	var name string
	if f.Name != nil {
		name = f.Name.Name
	}
	if ierr != nil {
		return name, imports, ierr
	}
	return name, imports, err
}

// importTable converts an *ast.File's Imports to [Import] entries —
// ParseFile's spelling, shared by the light header stages. A spec whose
// path literal will not unquote stops the conversion: the entries read
// so far come back with the error so a header-only caller can still use
// the partial table (ParseFile discards it either way).
func importTable(f *ast.File, filename string) ([]*Import, error) {
	var out []*Import
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return out, fmt.Errorf("invalid import path in %s: %w", filename, err)
		}
		var alias string
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		out = append(out, &Import{Path: path, Alias: alias, Pos: spec.Pos()})
	}
	return out, nil
}
