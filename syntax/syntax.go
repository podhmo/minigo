// Package syntax parses Go source files for the minigo interpreter.
//
// [ParseFile] wraps go/parser and returns a [File]: the *ast.File plus the
// file's [Import] table. Nothing here resolves or executes code; the
// [github.com/podhmo/minigo/compile] package turns the AST into bytecode.
//
// [CheckLang] applies the -lang gate, rejecting language features newer
// than a file's effective version ([File.Lang]); it takes the package-wide
// name table built by [DeclaredKinds].
//
// [TypeParamNames] and [RecvTypeParamNames] list the type parameter names
// a declaration binds.
package syntax

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
)

// File is a parsed Go source file plus its file-scoped import table.
// Create one with [ParseFile].
type File struct {
	Name    string // filename as given to the FileSet
	AST     *ast.File
	Imports []*Import // alias -> path entries, in declaration order
	// Src is the source text when it was provided in memory (REPL,
	// generated files); nil when the parser read the file from disk.
	// Traceback rendering uses it for source-line snippets.
	Src []byte
	// LangMod is the `go` directive of the module owning this file, as raw
	// directive text like "1.26.0" (see resolve.ModuleLang). "" means the
	// file lives outside any module — unversioned, so CheckLang leaves it
	// unbounded like `go run` on a module-less file. REPL inputs are never
	// versioned either.
	LangMod string
}

// Import is one import declaration.
type Import struct {
	Path  string // e.g. "fmt" or "github.com/x/y"
	Alias string // explicit name: "", "_", ".", or an identifier
	Pos   token.Pos
}

// ParseFile parses src, or reads filename when src is nil. On a syntax
// error it returns no [File] and an error listing all errors found. Every
// construct the toolchain's parser accepts is accepted here; constructs the
// interpreter does not support fail later, at run time. The filename is
// stored in [File] with forward slashes on every OS.
func ParseFile(fset *token.FileSet, filename string, src []byte) (*File, error) {
	// Positions, decl File fields, and traceback frames all quote this name:
	// normalize to "/" so script-visible paths match on every OS. Windows
	// still opens the slash form fine when src is nil.
	filename = filepath.ToSlash(filename)
	var srcAny any // typed-nil []byte would read as an empty file
	if src != nil {
		srcAny = src
	}
	f, err := parser.ParseFile(fset, filename, srcAny,
		parser.ParseComments|parser.SkipObjectResolution|parser.AllErrors)
	if err != nil {
		return nil, err
	}
	sf := &File{Name: filename, AST: f, Src: src}
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("invalid import path in %s: %w", filename, err)
		}
		var alias string
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		sf.Imports = append(sf.Imports, &Import{Path: path, Alias: alias, Pos: spec.Pos()})
	}
	return sf, nil
}

// LocalName returns the identifier the import binds in this file.
// For unnamed imports it approximates the package name by the path basename;
// a trailing "/vN" module-major-version element is stripped first.
// The true package name may differ.
func (i *Import) LocalName() string {
	if i.Alias != "" {
		return i.Alias
	}
	parts := strings.Split(i.Path, "/")
	name := parts[len(parts)-1]
	if len(name) >= 2 && name[0] == 'v' && isDigits(name[1:]) && len(parts) > 1 {
		name = parts[len(parts)-2]
	}
	return name
}

// TypeParamNames returns the names a type parameter list binds, in
// declaration order: `K comparable, V any` gives K, V. A nil list yields nil.
func TypeParamNames(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var out []string
	for _, fd := range fl.List {
		for _, n := range fd.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// RecvTypeParamNames returns the type parameter names a method receiver
// declares, in order: `(p *Pair[A, B])` binds A and B for the whole method
// body. A non-generic receiver yields nil.
func RecvTypeParamNames(recv *ast.FieldList) []string {
	if recv == nil || len(recv.List) == 0 {
		return nil
	}
	e := recv.List[0].Type
peel:
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			break peel
		}
	}
	var out []string
	add := func(x ast.Expr) {
		if id, ok := x.(*ast.Ident); ok {
			out = append(out, id.Name)
		}
	}
	switch x := e.(type) {
	case *ast.IndexExpr:
		add(x.Index)
	case *ast.IndexListExpr:
		for _, ix := range x.Indices {
			add(ix)
		}
	}
	return out
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
