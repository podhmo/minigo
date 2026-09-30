// Package index builds a PackageIndex from parsed files: declarations are
// recorded but never evaluated. Evaluation happens at package Initialize or
// function CALL time only.
package index

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/syntax"
)

// Kind classifies a top-level declaration.
type Kind int

const (
	FuncDecl Kind = iota
	VarDecl
	ConstDecl
	TypeDecl
)

// Decl is a single named top-level declaration (one name in a spec).
type Decl struct {
	Kind Kind
	Name string
	File *syntax.File

	Func      *ast.FuncDecl // Kind == FuncDecl (methods go to TypeDecl.Methods)
	Gen       *ast.GenDecl  // Kind == VarDecl/ConstDecl/TypeDecl
	Spec      ast.Spec      // *ast.ValueSpec or *ast.TypeSpec
	Idx       int           // spec index within Gen (iota)
	NameIdx   int           // index of this name within Spec.Names
	Inherited []ast.Expr    // const spec: previous spec's values when empty
	Pos       token.Pos
}

// TypeDeclInfo is a named type plus the methods declared on it.
type TypeDeclInfo struct {
	Decl    *Decl
	Methods map[string]*Decl // method name -> Func decl (receiver this type)
}

// Index is the per-package declaration table.
type Index struct {
	Decls  []*Decl          // all decls in file order (init order)
	Funcs  map[string]*Decl // non-method functions, incl. init
	Types  map[string]*TypeDeclInfo
	Vars   map[string]*Decl
	Consts map[string]*Decl
	Inits  []*Decl // init() functions in order
}

// Build indexes all files of one package. Nothing is executed.
func Build(files []*syntax.File) (*Index, error) {
	ix := &Index{
		Funcs:  map[string]*Decl{},
		Types:  map[string]*TypeDeclInfo{},
		Vars:   map[string]*Decl{},
		Consts: map[string]*Decl{},
	}
	var methodDecls []*Decl
	for _, f := range files {
		for _, gd := range f.AST.Decls {
			switch d := gd.(type) {
			case *ast.FuncDecl:
				dl := &Decl{Kind: FuncDecl, Name: d.Name.Name, File: f, Func: d, Pos: d.Pos()}
				if d.Recv != nil {
					methodDecls = append(methodDecls, dl)
					continue
				}
				ix.Decls = append(ix.Decls, dl)
				if d.Name.Name == "init" {
					ix.Inits = append(ix.Inits, dl)
					continue
				}
				ix.Funcs[dl.Name] = dl
			case *ast.GenDecl:
				var prevValues []ast.Expr
				for i, spec := range d.Specs {
					switch d.Tok {
					case token.VAR, token.CONST:
						vs := spec.(*ast.ValueSpec)
						inherited := []ast.Expr(nil)
						if d.Tok == token.CONST && len(vs.Values) == 0 {
							inherited = prevValues
						} else {
							prevValues = vs.Values
						}
						for j, name := range vs.Names {
							kind := VarDecl
							if d.Tok == token.CONST {
								kind = ConstDecl
							}
							dl := &Decl{Kind: kind, Name: name.Name, File: f, Gen: d, Spec: vs, Idx: i, NameIdx: j, Inherited: inherited, Pos: name.Pos()}
							ix.Decls = append(ix.Decls, dl)
							if kind == VarDecl {
								ix.Vars[name.Name] = dl
							} else {
								ix.Consts[name.Name] = dl
							}
						}
					case token.TYPE:
						ts := spec.(*ast.TypeSpec)
						dl := &Decl{Kind: TypeDecl, Name: ts.Name.Name, File: f, Gen: d, Spec: ts, Idx: i, Pos: ts.Pos()}
						ix.Decls = append(ix.Decls, dl)
						ix.Types[ts.Name.Name] = &TypeDeclInfo{Decl: dl, Methods: map[string]*Decl{}}
					case token.IMPORT:
						// already collected into syntax.File.Imports
					default:
						return nil, fmt.Errorf("unexpected GenDecl token %s", d.Tok)
					}
				}
			}
		}
	}
	// Attach methods to their receiver type.
	for _, md := range methodDecls {
		tname := ReceiverTypeName(md.Func.Recv)
		if tname == "" {
			continue
		}
		td, ok := ix.Types[tname]
		if !ok {
			// method on a type declared elsewhere (e.g. other file already
			// indexed — within a package all files are indexed together, so
			// this is a foreign receiver): record under a synthetic entry.
			td = &TypeDeclInfo{Methods: map[string]*Decl{}}
			ix.Types[tname] = td
		}
		td.Methods[md.Name] = md
	}
	return ix, nil
}

// ReceiverTypeName extracts the base type name from a receiver like
// `t T`, `t *T`, `t T[P]`.
func ReceiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	t := recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.StarExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		default:
			return ""
		}
	}
}
