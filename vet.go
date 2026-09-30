// Vet pass: lists call sites into stub packages that are neither registered
// special forms nor bound host symbols. A stub member's body is
// `panic("minigo intrinsic")` — reaching it at run time is a loud but late
// failure, so this checker surfaces the same mistake statically (plan §16).
package minigo

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

// VetResult is one vet finding: an unregistered call into a stub member.
type VetResult struct {
	Pos      token.Position
	Call     string // "pkg.Sym" as written (alias.Sym form)
	Resolved string // canonical "import/path.Sym" it resolves to
}

func (r VetResult) String() string {
	return fmt.Sprintf("%s: call %s resolves to unregistered stub member %s", r.Pos, r.Call, r.Resolved)
}

// Vet loads ref (without initializing it) and reports every call site
// `pkgAlias.Sym(...)` where Sym's declaration is a stub (body is exactly
// `panic("minigo intrinsic")`) and the symbol is neither a registered
// special form nor a bound host symbol on this engine. Dot imports are not
// tracked — a `Sym(...)` call into a dot-imported stub package is invisible
// to the checker. Function-scoped shadowing is handled conservatively: a
// name declared anywhere inside the enclosing function (even a nested
// literal's params) hides the import binding for that whole function —
// so a shadowed alias may miss a real finding but never reports a local
// variable's method call as a stub call.
func (e *Engine) Vet(ctx context.Context, ref string) ([]VetResult, error) {
	p, err := e.Package(ctx, ref)
	if err != nil {
		return nil, err
	}
	var out []VetResult
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
			e.scanStubCalls(ctx, decl, imports, shadow, &out)
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
func (e *Engine) scanStubCalls(ctx context.Context, root ast.Node, imports map[string]string, shadow map[string]bool, out *[]VetResult) {
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
			e.scanStubCalls(ctx, lit, imports, merged, out)
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
		if e.registeredVetSymbol(path, name) {
			return true
		}
		if !e.stubMember(ctx, path, name) {
			return true
		}
		*out = append(*out, VetResult{
			Pos:      e.fset.Position(call.Lparen),
			Call:     x.Name + "." + name,
			Resolved: path + "." + name,
		})
		return true
	})
}

// registeredVetSymbol reports whether path.Name is already intercepted:
// a special form the compiler emits OpSpecialCall for, or a symbol in a
// host-bound package.
func (e *Engine) registeredVetSymbol(path, name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.specials[runtime.SymbolID{PackagePath: path, Name: name}]; ok {
		return true
	}
	if b, ok := e.binds[path]; ok {
		if _, ok := b.Globals.Get(name); ok {
			return true
		}
	}
	return false
}

// stubMember reports whether path.Name resolves to a stub declaration: a
// function whose body is exactly `panic("minigo intrinsic")`. The callee
// package is located and indexed but never initialized; packages that
// cannot be located answer false (nothing to inspect).
func (e *Engine) stubMember(ctx context.Context, path, name string) bool {
	p, err := e.Package(ctx, path)
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
