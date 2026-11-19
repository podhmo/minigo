// Package vm is the minigo stack-machine interpreter: it runs the
// [bytecode.Chunk] code produced by [github.com/podhmo/minigo/compile].
//
// The host creates a [VM] with its [Hooks] filled in — the engine services
// the VM needs, such as builtins and lazy materialization of package
// members. [VM.Call] invokes a function-like value and is the error
// boundary: script panics and traps come back as errors. A *VM implements
// [runtime.VMCaller], through which host builtins call back into the script.
//
// Each Call runs inside a script process that the `go` statement's
// goroutines share. By default the outermost Call opens the process and
// ends it on return, stopping goroutines still running; to keep one
// process across several Calls, hold it with [VM.EnsureProc] instead.
//
// The process reports a deadlock when all its goroutines are parked with
// nothing left to wake them. Host intrinsics that will call into the
// script later, such as timers, must report the pending callback, or the
// waiting process may be reported as deadlocked.
package vm

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"strings"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// specialCtx implements runtime.SpecialContext: the surface a special-form
// handler sees of the caller's frame.
type specialCtx struct {
	v *VM
	f *frame
	q *runtime.QuotedCall
}

func (s *specialCtx) Position(n ast.Node) token.Position {
	if s.f.fn.Pkg != nil && s.f.fn.Pkg.Fset != nil {
		return s.f.fn.Pkg.Fset.Position(n.Pos())
	}
	return token.Position{}
}

func (s *specialCtx) File() *syntax.File { return s.q.File }

func (s *specialCtx) Package() *runtime.Package { return s.f.fn.Pkg }

func (s *specialCtx) Format(n ast.Node) string {
	var b strings.Builder
	var fset *token.FileSet
	if s.f.fn.Pkg != nil {
		fset = s.f.fn.Pkg.Fset
	}
	if fset == nil {
		fset = token.NewFileSet()
	}
	if err := format.Node(&b, fset, n); err != nil {
		return fmt.Sprintf("<bad node %T>", n)
	}
	return b.String()
}

func (s *specialCtx) Call(fn runtime.Value, args []runtime.Value) (runtime.Value, error) {
	return s.v.Call(fn, args)
}

// ResolveSymbol maps an expression to its canonical SymbolID through the
// caller file's import scope — no package is materialized, matching the
// plan's index-level laziness for special forms.
func (s *specialCtx) ResolveSymbol(e ast.Expr) (runtime.SymbolID, error) {
	pkg := s.f.fn.Pkg
	switch x := e.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok {
			return runtime.SymbolID{}, fmt.Errorf("cannot resolve %s to a symbol", s.Format(e))
		}
		// a local or captured variable may shadow an import name: selector
		// expressions on it are member access, not package symbols
		if _, ok := s.q.Locals[id.Name]; ok {
			return runtime.SymbolID{}, s.Errorf(x.X, "%s is a local variable, not an import alias", id.Name)
		}
		if _, ok := s.q.Upvals[id.Name]; ok {
			return runtime.SymbolID{}, s.Errorf(x.X, "%s is a captured variable, not an import alias", id.Name)
		}
		if pkg != nil {
			if refs, ok := pkg.Scopes[s.q.File]; ok {
				if ref, ok := refs[id.Name]; ok {
					return runtime.SymbolID{PackagePath: ref.Path, Name: x.Sel.Name}, nil
				}
			}
		}
		return runtime.SymbolID{}, s.Errorf(x, "%s is not an import alias in this file", id.Name)
	case *ast.Ident:
		if _, ok := s.q.Locals[x.Name]; ok {
			return runtime.SymbolID{}, s.Errorf(x, "%s is a local variable, not a package symbol", x.Name)
		}
		if _, ok := s.q.Upvals[x.Name]; ok {
			return runtime.SymbolID{}, s.Errorf(x, "%s is a captured variable, not a package symbol", x.Name)
		}
		path := ""
		if pkg != nil {
			path = pkg.Path
		}
		return runtime.SymbolID{PackagePath: path, Name: x.Name}, nil
	default:
		return runtime.SymbolID{}, s.Errorf(e, "cannot resolve %T to a symbol", e)
	}
}

// Resolve maps a symbol expression to its runtime value through ordinary
// name resolution — locals/upvals first, then globals (materializing only
// the named decl), then a pkg.Sym selector via the file's import table.
// Non-symbol expressions are rejected, keeping resolution declaration-
// level lazy: no arbitrary code runs that Eval would allow.
func (s *specialCtx) Resolve(e ast.Expr) (runtime.Value, error) {
	switch x := e.(type) {
	case *ast.Ident:
		if slot, ok := s.q.Locals[x.Name]; ok {
			if slot >= len(s.f.locals) || s.f.locals[slot] == nil {
				return nil, s.Errorf(x, "local %s is not bound", x.Name)
			}
			return s.f.localVal(slot), nil
		}
		if idx, ok := s.q.Upvals[x.Name]; ok {
			if idx >= len(s.f.upvals) || s.f.upvals[idx] == nil {
				return nil, s.Errorf(x, "upvalue %s is not bound", x.Name)
			}
			return s.f.upvals[idx].Elem, nil
		}
		mv, err := s.v.resolveGlobalE(s.f, x.Name)
		if err != nil {
			return nil, s.Errorf(x, "%s", err)
		}
		return mv, nil
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok {
			return nil, s.Errorf(e, "cannot resolve %s to a value", s.Format(e))
		}
		if _, ok := s.q.Locals[id.Name]; ok {
			return nil, s.Errorf(x.X, "%s is a local variable, not an import alias", id.Name)
		}
		if _, ok := s.q.Upvals[id.Name]; ok {
			return nil, s.Errorf(x.X, "%s is a captured variable, not an import alias", id.Name)
		}
		pkg := s.f.fn.Pkg
		if pkg == nil {
			return nil, s.Errorf(x, "no package context")
		}
		ref, ok := pkg.Scopes[s.q.File][id.Name]
		if !ok {
			return nil, s.Errorf(x, "%s is not an import alias in this file", id.Name)
		}
		p, err := ref.Materialize()
		if err != nil {
			return nil, err
		}
		mv, err := s.v.memberOf(p, x.Sel.Name)
		if err != nil {
			return nil, err
		}
		if c, isCell := mv.(*runtime.Cell); isCell {
			return c.Elem, nil
		}
		return mv, nil
	default:
		return nil, s.Errorf(e, "cannot resolve %T to a value", e)
	}
}

// ResolveType resolves a type expression to its *TypeDef: named types
// through Resolve, composite forms as the same anonymous typedefs the
// compiler emits for declared types (Anon specs keep element types
// lazily resolved on use).
func (s *specialCtx) ResolveType(e ast.Expr) (*runtime.TypeDef, error) {
	pkg := s.f.fn.Pkg
	switch t := e.(type) {
	case *ast.Ident:
		// Inside a generic instantiation a bare ident may name a type
		// parameter: resolve it through the function's binds first.
		if td, ok := s.f.fn.Binds[t.Name]; ok {
			if td2, ok := td.(*runtime.TypeDef); ok {
				return td2, nil
			}
			return nil, s.Errorf(e, "%s is bound to a non-type", t.Name)
		}
		mv, err := s.Resolve(e)
		if err != nil {
			return nil, err
		}
		td, ok := mv.(*runtime.TypeDef)
		if !ok {
			return nil, s.Errorf(e, "%s is not a type", s.Format(e))
		}
		return td, nil
	case *ast.SelectorExpr:
		mv, err := s.Resolve(e)
		if err != nil {
			return nil, err
		}
		td, ok := mv.(*runtime.TypeDef)
		if !ok {
			return nil, s.Errorf(e, "%s is not a type", s.Format(e))
		}
		return td, nil
	case *ast.ArrayType:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.MapType:
		return &runtime.TypeDef{Kind: runtime.KindMap, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.StarExpr:
		return &runtime.TypeDef{Kind: runtime.KindPointer, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.ChanType:
		return &runtime.TypeDef{Kind: runtime.KindChan, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.FuncType:
		return &runtime.TypeDef{Kind: runtime.KindFunc, Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	case *ast.StructType:
		td := &runtime.TypeDef{Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}
		td.SetStructShape(t)
		return td, nil
	case *ast.InterfaceType:
		td := &runtime.TypeDef{Anon: t, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}
		td.SetInterfaceShape(t)
		return td, nil
	case *ast.ParenExpr:
		return s.ResolveType(t.X)
	case *ast.IndexExpr:
		return s.instantiateType(e, t.X, []ast.Expr{t.Index})
	case *ast.IndexListExpr:
		return s.instantiateType(e, t.X, t.Indices)
	case *ast.Ellipsis:
		return &runtime.TypeDef{Kind: runtime.KindSlice, Anon: &ast.ArrayType{Lbrack: t.Pos(), Elt: t.Elt}, Pkg: pkg, File: s.q.File, Binds: s.f.fn.Binds}, nil
	default:
		return nil, s.Errorf(e, "unsupported type expression %T", e)
	}
}

// Eval compiles expr against the caller's live scope (locals/upvals snap-
// shotted at the special call site) and runs it in a fresh frame sharing
// the caller's cells — writes by the quoted expr are visible to the caller.
func (s *specialCtx) Eval(e ast.Expr) (runtime.Value, error) {
	if s.v.H.CompileScopedExpr == nil {
		return nil, fmt.Errorf("no scoped-expression compiler")
	}
	ch, err := s.v.H.CompileScopedExpr(s.f.fn.Pkg, s.q.File, e, s.q.Locals, s.q.Upvals)
	if err != nil {
		return nil, err
	}
	fr := &frame{
		fn:     &runtime.Function{Pkg: s.f.fn.Pkg, File: s.q.File, Name: "<special-eval>"},
		ch:     ch,
		locals: make([]runtime.Value, ch.NLocals),
		ltyps:  make([]*runtime.TypeDef, ch.NLocals),
		upvals: s.f.upvals,
		v:      s.v,
		base:   len(s.v.vals),
	}
	fr.stack = s.v.vals[fr.base:]
	// share the caller's slots — slot indices line up by construction;
	// the locals a quoted form can name are all boxed (P4-2a), so they
	// arrive as shared cell pointers and writes propagate back.
	n := len(s.f.locals)
	if n > len(fr.locals) {
		n = len(fr.locals)
	}
	copy(fr.locals, s.f.locals[:n])
	copy(fr.ltyps, s.f.ltyps[:min(n, len(s.f.ltyps))])
	// the shared cells can now escape through this eval — disqualify the
	// caller's block from recycling.
	s.f.cellsShared = true
	for i := n; i < len(fr.locals); i++ {
		fr.locals[i] = &localCell{&runtime.Cell{Elem: runtime.NIL}}
	}
	s.v.exec(fr)
	if fr.sp == 0 {
		return runtime.NIL, nil
	}
	return fr.stack[fr.sp-1], nil
}

func (s *specialCtx) Errorf(n ast.Node, formatStr string, args ...any) error {
	return fmt.Errorf("%s: %s", s.Position(n), fmt.Sprintf(formatStr, args...))
}
