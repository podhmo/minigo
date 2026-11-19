// Vet pass: lists call sites into stub packages that are neither registered
// special forms nor bound host symbols. A stub member's body is
// `panic("minigo intrinsic")` — reaching it at run time is a loud but late
// failure, so this checker surfaces the same mistake statically (plan §16).

package vet

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strconv"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/runtime"
)

// stubPanic is the marker body of a stub-package member: a declaration that
// exists only to satisfy gofmt/gopls and is meant to be intercepted by a
// special form or a bound host symbol.
const stubPanic = "minigo intrinsic"

// Deps wires the scanner to the engine: Package resolves an import path
// (indexed, not initialized), Position maps a token.Pos, Registered
// reports symbols handled by special forms or host bindings.
type Deps struct {
	Package    func(ctx context.Context, path string) (*runtime.Package, error)
	Position   func(pos token.Pos) token.Position
	Registered func(path, name string) bool
}

// Result is one [Scan] finding.
type Result struct {
	Pos      token.Position
	Call     string // "pkg.Sym" as written (alias.Sym form)
	Resolved string // canonical "import/path.Sym" it resolves to
}

func (r Result) String() string {
	return fmt.Sprintf("%s: call %s resolves to unregistered stub member %s", r.Pos, r.Call, r.Resolved)
}

// Scan checks an already-loaded package for calls that would fail at
// run time because nothing handles them: `pkgAlias.Sym(...)` where Sym
// is a stub (its body is exactly `panic("minigo intrinsic")`) that is
// neither a registered special form nor a bound host symbol. Calls
// through dot imports are not checked, and an import name shadowed
// anywhere in a function is not checked in that function.
func Scan(ctx context.Context, p *runtime.Package, deps Deps) ([]Result, error) {
	var out []Result
	for _, sf := range p.Files {
		imports := map[string]string{} // local name -> import path
		for _, im := range sf.Imports {
			if im.Alias == "_" || im.Alias == "." {
				continue
			}
			imports[im.LocalName()] = im.Path
		}
		for _, decl := range sf.AST.Decls {
			var shadow map[string]bool
			if fd, ok := decl.(*ast.FuncDecl); ok {
				shadow = declaredNames(fd)
			}
			scanStubCalls(ctx, decl, imports, shadow, &out, deps)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Pos.Filename < out[j].Pos.Filename ||
			(out[i].Pos.Filename == out[j].Pos.Filename && out[i].Pos.Line < out[j].Pos.Line)
	})
	return out, nil
}

// declaredNames collects every identifier a function subtree declares:
// receiver/param/result names, := targets, local var/const/type names, and
// range variables — including declarations inside nested function
// literals, so the set over-approximates shadowing.
func declaredNames(root ast.Node) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(root, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.FuncType:
			for _, fl := range []*ast.FieldList{d.Params, d.Results} {
				if fl == nil {
					continue
				}
				for _, fld := range fl.List {
					for _, nm := range fld.Names {
						out[nm.Name] = true
					}
				}
			}
		case *ast.AssignStmt:
			if d.Tok == token.DEFINE {
				for _, lhs := range d.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch s := sp.(type) {
				case *ast.ValueSpec:
					for _, nm := range s.Names {
						out[nm.Name] = true
					}
				case *ast.TypeSpec:
					out[s.Name.Name] = true
				}
			}
		case *ast.RangeStmt:
			if d.Tok == token.DEFINE {
				for _, e := range []ast.Expr{d.Key, d.Value} {
					if id, ok := e.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
		}
		return true
	})
	if fd, ok := root.(*ast.FuncDecl); ok && fd.Recv != nil {
		for _, fld := range fd.Recv.List {
			for _, nm := range fld.Names {
				out[nm.Name] = true
			}
		}
	}
	return out
}

// scanStubCalls inspects one declaration subtree for `alias.Sym(...)` calls
// that reach unregistered stub members, skipping selector bases hidden by
// the shadow set.
func scanStubCalls(ctx context.Context, root ast.Node, imports map[string]string, shadow map[string]bool, out *[]Result, deps Deps) {
	ast.Inspect(root, func(n ast.Node) bool {
		if lit, ok := n.(*ast.FuncLit); ok && root != lit {
			// A nested function literal introduces its own scope: scan it
			// with the union of its declared names and the outer set.
			merged := map[string]bool{}
			for k := range shadow {
				merged[k] = true
			}
			for k := range declaredNames(lit) {
				merged[k] = true
			}
			scanStubCalls(ctx, lit, imports, merged, out, deps)
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if shadow[x.Name] {
			return true // a function-local name hides the import binding
		}
		path, ok := imports[x.Name]
		if !ok {
			return true // method call or local value selector
		}
		name := sel.Sel.Name
		if deps.Registered(path, name) {
			return true
		}
		if !stubMember(ctx, path, name, deps) {
			return true
		}
		*out = append(*out, Result{
			Pos:      deps.Position(call.Lparen),
			Call:     x.Name + "." + name,
			Resolved: path + "." + name,
		})
		return true
	})
}

// stubMember reports whether path.Name resolves to a stub declaration: a
// function whose body is exactly `panic("minigo intrinsic")`. The callee
// package is located and indexed but never initialized; packages that
// cannot be located answer false (nothing to inspect).
func stubMember(ctx context.Context, path, name string, deps Deps) bool {
	p, err := deps.Package(ctx, path)
	if err != nil || p.Index == nil {
		return false
	}
	d, ok := p.Index.Funcs[name]
	if !ok || d.Kind != index.FuncDecl || d.Func == nil || d.Func.Body == nil {
		return false
	}
	if len(d.Func.Body.List) != 1 {
		return false
	}
	stmt, ok := d.Func.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	fn, ok := call.Fun.(*ast.Ident)
	if !ok || fn.Name != "panic" || len(call.Args) != 1 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	msg, err := strconv.Unquote(lit.Value)
	return err == nil && msg == stubPanic
}
