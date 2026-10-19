// Package index records the top-level declarations of one package's
// parsed files without evaluating them; evaluation happens later, at
// package initialization or call time.
//
// [Build] indexes a package and rejects redeclared names as gc does;
// [BuildLenient] lets a later declaration replace an earlier one, as a
// REPL session needs.
package index

import (
	"fmt"
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/syntax"
)

// Kind classifies a [Decl].
type Kind int

const (
	FuncDecl Kind = iota
	VarDecl
	ConstDecl
	TypeDecl
)

// Decl is a single named top-level declaration: one name of a spec, or
// one function or method.
type Decl struct {
	Kind Kind
	Name string
	File *syntax.File

	Func      *ast.FuncDecl // Kind == FuncDecl (methods are listed in TypeDeclInfo.Methods)
	Gen       *ast.GenDecl  // Kind == VarDecl/ConstDecl/TypeDecl
	Spec      ast.Spec      // *ast.ValueSpec or *ast.TypeSpec
	Idx       int           // spec index within Gen (iota)
	NameIdx   int           // index of this name within Spec.Names
	Inherited []ast.Expr    // const spec: previous spec's values when empty
	// InheritedType is the type the values came from — Go substitutes
	// "the first preceding non-empty expression list and its type if
	// any", so `B` under `A T = e` is really `B T = e`.
	InheritedType ast.Expr
	Pos           token.Pos
}

// TypeDeclInfo is a named type plus the methods declared on it. Decl is
// nil when the files declare methods on a type name they do not declare.
type TypeDeclInfo struct {
	Decl    *Decl
	Methods map[string]*Decl // method name -> Func decl (receiver this type)
}

// Index is the declaration table of one package. Maps are keyed by
// declared name; under [BuildLenient] a later duplicate replaces an
// earlier one.
type Index struct {
	Decls   []*Decl          // non-method decls in source order, files in the order given
	Methods []*Decl          // method decls in source order — every declaration, including same-name duplicates the per-type Methods maps drop
	Funcs   map[string]*Decl // non-method functions, excluding init
	Types   map[string]*TypeDeclInfo
	Vars    map[string]*Decl
	Consts  map[string]*Decl
	Inits   []*Decl // init() functions in order

	// LocalGens numbers each non-alias type declared inside a function
	// body, from 1 in package source order, as gc does; the number appears
	// in gc's spelling of instantiations (`main.U[int;int]·3`). Read it
	// with LocalGen.
	LocalGens map[*ast.TypeSpec]int
}

// LocalGen returns ts's number in LocalGens, 0 when ts is not a
// function-local non-alias type decl of this package.
func (ix *Index) LocalGen(ts *ast.TypeSpec) int {
	return ix.LocalGens[ts]
}

// RedeclError reports a duplicate package-block name from [Build] —
// Go's `X redeclared in this block` compile error. Pos points at the
// second declaration, PrevPos at the first; callers render them through
// their FileSet.
type RedeclError struct {
	Name    string
	Pos     token.Pos
	PrevPos token.Pos
	// Method marks a duplicate method declaration; Name is then "T.M".
	// gc words this case differently from a package-block redeclaration.
	Method bool
}

func (e *RedeclError) Error() string {
	return e.Name + " redeclared in this block"
}

// Build indexes all files of one package. A name declared twice in the
// package block, or a method declared twice on a type, fails with a
// [*RedeclError]; init and the blank identifier may repeat.
func Build(files []*syntax.File) (*Index, error) {
	return build(files, false)
}

// BuildLenient is [Build] where a later declaration of a name replaces an
// earlier one instead of failing, as in a REPL session where a re-entered
// declaration replaces the old one.
func BuildLenient(files []*syntax.File) (*Index, error) {
	// The REPL's accumulated prompt source legitimately redeclares, while
	// :load units enforce redeclaration at their own boundary.
	return build(files, true)
}

func build(files []*syntax.File, lenient bool) (*Index, error) {
	ix := &Index{
		Funcs:  map[string]*Decl{},
		Types:  map[string]*TypeDeclInfo{},
		Vars:   map[string]*Decl{},
		Consts: map[string]*Decl{},
	}
	var methodDecls []*Decl
	// package-block names share one namespace across funcs, vars, consts
	// and types; init and the blank identifier are exempt.
	declared := map[string]token.Pos{}
	gen := 0
	for _, f := range files {
		for _, gd := range f.AST.Decls {
			if fd, ok := gd.(*ast.FuncDecl); ok && fd.Body != nil {
				// number non-alias function-local type decls in source
				// order — including inside nested function literals — for
				// the `·gen` spelling inside instantiation arg lists.
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if ts, isTs := n.(*ast.TypeSpec); isTs && !ts.Assign.IsValid() {
						gen++
						if ix.LocalGens == nil {
							ix.LocalGens = map[*ast.TypeSpec]int{}
						}
						ix.LocalGens[ts] = gen
					}
					return true
				})
			}
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
				if !lenient && dl.Name != "_" {
					// `func _` (stringer-style compile stubs) declares
					// no name; gc never reports it redeclared. The name's
					// position is what gc reports, not the `func` keyword's.
					if prev, dup := declared[dl.Name]; dup {
						return nil, &RedeclError{Name: dl.Name, Pos: d.Name.Pos(), PrevPos: prev}
					}
					declared[dl.Name] = d.Name.Pos()
				}
				ix.Funcs[dl.Name] = dl
			case *ast.GenDecl:
				switch d.Tok {
				case token.VAR, token.CONST:
					for i, effective := range ValueSpecs(d) {
						vs := effective.Spec
						var inherited []ast.Expr
						var inheritedType ast.Expr
						if d.Tok == token.CONST && len(vs.Values) == 0 {
							inherited, inheritedType = effective.Values, effective.Type
						}
						for j, name := range vs.Names {
							kind := VarDecl
							if d.Tok == token.CONST {
								kind = ConstDecl
							}
							dl := &Decl{Kind: kind, Name: name.Name, File: f, Gen: d, Spec: vs, Idx: i, NameIdx: j, Inherited: inherited, InheritedType: inheritedType, Pos: name.Pos()}
							ix.Decls = append(ix.Decls, dl)
							if name.Name != "_" && !lenient {
								if prev, dup := declared[name.Name]; dup {
									return nil, &RedeclError{Name: name.Name, Pos: name.Pos(), PrevPos: prev}
								}
								declared[name.Name] = name.Pos()
							}
							if kind == VarDecl {
								ix.Vars[name.Name] = dl
							} else {
								ix.Consts[name.Name] = dl
							}
						}
					}
				case token.TYPE:
					for i, spec := range d.Specs {
						ts := spec.(*ast.TypeSpec)
						dl := &Decl{Kind: TypeDecl, Name: ts.Name.Name, File: f, Gen: d, Spec: ts, Idx: i, Pos: ts.Pos()}
						ix.Decls = append(ix.Decls, dl)
						if ts.Name.Name != "_" && !lenient {
							if prev, dup := declared[ts.Name.Name]; dup {
								return nil, &RedeclError{Name: ts.Name.Name, Pos: ts.Name.Pos(), PrevPos: prev}
							}
							declared[ts.Name.Name] = ts.Name.Pos()
						}
						ix.Types[ts.Name.Name] = &TypeDeclInfo{Decl: dl, Methods: map[string]*Decl{}}
					}
				case token.IMPORT:
					// already collected into syntax.File.Imports
				default:
					return nil, fmt.Errorf("unexpected GenDecl token %s", d.Tok)
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
		if !lenient {
			key := tname + "." + md.Name
			if prev, dup := declared[key]; dup {
				return nil, &RedeclError{Name: key, Pos: md.Func.Name.Pos(), PrevPos: prev, Method: true}
			}
			declared[key] = md.Func.Name.Pos()
		}
		td.Methods[md.Name] = md
	}
	ix.Methods = methodDecls
	return ix, nil
}

// ReceiverTypeName returns the base type name of a receiver like `t T`,
// `t *T` or `t T[P]`, or "" when recv has no recognizable receiver type.
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
