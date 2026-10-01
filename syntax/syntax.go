// Package syntax is the front end of minigo: a thin wrapper over go/parser
// producing *ast.File plus a per-file import table. Nothing here executes or
// resolves anything; the compiler is deliberately a total function over the
// AST so that every valid Go file parses.
package syntax

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// File is a parsed Go source file plus its file-scoped import table.
type File struct {
	Name    string // filename as given to the FileSet
	AST     *ast.File
	Imports []*Import // alias -> path entries, in declaration order
	// Src is the source text when it was provided in memory (REPL,
	// generated files); nil when the parser read the file from disk.
	// Traceback rendering uses it for source-line snippets.
	Src []byte
}

// Import is one import declaration.
type Import struct {
	Path  string // e.g. "fmt" or "github.com/x/y"
	Alias string // explicit name: "", "_", ".", or an identifier
	Pos   token.Pos
}

// ParseFile parses src. It never returns a partial AST silently: syntax
// errors are reported, but every construct the toolchain's parser accepts is
// accepted here (unsupported constructs become runtime TRAPs downstream).
func ParseFile(fset *token.FileSet, filename string, src []byte) (*File, error) {
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
// (The true package name may differ; cheap metadata resolution is a known
// follow-up — see the design doc.)
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
