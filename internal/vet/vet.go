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
	"github.com/podhmo/minigo/pkg/astwalk"
	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
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
		locals := syntax.NewLocalScopes(sf.AST)
		refs := map[int]*runtime.ImportRef{} // import's position in sf.Imports -> its engine ref
		if rl := p.Imports[sf]; rl != nil {
			for i, im := range sf.Imports {
				if i < len(rl) && rl[i] != nil && rl[i].Path == im.Path {
					refs[i] = rl[i]
				}
			}
		}
		imports := map[string]string{} // local name -> import path
		for i, im := range sf.Imports {
			if im.Alias == "_" || im.Alias == "." {
				continue
			}
			name := im.LocalName()
			if im.Alias == "" {
				// an unaliased import binds the declared package name,
				// which may differ from the path tail (gopkg.in/yaml.v3
				// declares yaml).
				if ref := refs[i]; ref != nil {
					if n := ref.PackageName(); n != "" {
						name = n
					}
				}
			}
			imports[name] = im.Path
		}
		for _, decl := range sf.AST.Decls {
			scanStubCalls(ctx, decl, imports, locals, &out, deps)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Pos.Filename < out[j].Pos.Filename ||
			(out[i].Pos.Filename == out[j].Pos.Filename && out[i].Pos.Line < out[j].Pos.Line)
	})
	return out, nil
}

// scanStubCalls inspects one declaration subtree for `alias.Sym(...)` calls
// that reach unregistered stub members, skipping selector bases a local
// binding hides at the call's position.
func scanStubCalls(ctx context.Context, root ast.Node, imports map[string]string, locals *syntax.LocalScopes, out *[]Result, deps Deps) {
	for call := range astwalk.Each[*ast.CallExpr](root) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			continue
		}
		if locals.At(x.Name, x.Pos()) {
			continue // a local binding hides the import here
		}
		path, ok := imports[x.Name]
		if !ok {
			continue // method call or local value selector
		}
		name := sel.Sel.Name
		if deps.Registered(path, name) {
			continue
		}
		if !stubMember(ctx, path, name, deps) {
			continue
		}
		*out = append(*out, Result{
			Pos:      deps.Position(call.Lparen),
			Call:     x.Name + "." + name,
			Resolved: path + "." + name,
		})
	}
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
