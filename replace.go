package minigo

import (
	"context"
	"fmt"
	"go/ast"
	"reflect"

	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/internal/typedef"
	"github.com/podhmo/minigo/runtime"
)

// Member returns one package member without running it. Loading the
// package is lazy in the usual way: reading a function does not initialize
// the package, and reading a var or const does not force initialization
// either. Use [Engine.Call] when Go's initialization guarantee is needed.
func (e *Engine) Member(ctx context.Context, ref, name string) (runtime.Value, error) {
	pkg, err := e.Package(ctx, ref)
	if err != nil {
		return nil, err
	}
	return pkg.Member(name, e.materialize)
}

// ReplaceSourceFunc replaces a package-level source function before the
// package initializes. ref is the source package reference Engine.Package
// accepts and name is a non-generic function declared there. replacement
// may be a script function value (for example one read with [Engine.Member])
// whose signature is identical to the source declaration, a Go func, or a
// *runtime.BuiltinFunc with a Go Target. Go functions use the source-call
// result convention: a trailing error remains a script result value.
//
// For a Go replacement, predeclared basic parameter and result types must
// match exactly; every source type outside that set is represented as any.
// Register replacements before running or otherwise materializing the
// package. Direct calls, package-qualified calls and function values read
// the replacement. Method dispatch and calls through already-compiled host
// code are outside this mechanism, and replacements do not rewrite imports
// of a host-bound package.
func (e *Engine) ReplaceSourceFunc(ctx context.Context, ref, name string, replacement any) error {
	if name == "" {
		return fmt.Errorf("ReplaceSourceFunc: empty function name")
	}
	pkg, err := e.Package(ctx, ref)
	if err != nil {
		return err
	}
	if pkg.Index == nil {
		return fmt.Errorf("ReplaceSourceFunc %s.%s: %s is not a source package", pkg.Path, name, pkg.Path)
	}
	if st := pkg.State(); st != runtime.Parsed && st != runtime.Indexed {
		return fmt.Errorf("ReplaceSourceFunc %s.%s: package is already initializing", pkg.Path, name)
	}
	v, err := e.sourceReplacementValue(pkg, name, replacement)
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	reps := e.srcReplacements[pkg.Path]
	_, replacing := reps[name]
	if !replacing {
		if _, ok := pkg.Globals.Get(name); ok {
			return fmt.Errorf("ReplaceSourceFunc %s.%s: function is already materialized", pkg.Path, name)
		}
	}
	if reps == nil {
		reps = map[string]runtime.Value{}
		e.srcReplacements[pkg.Path] = reps
	}
	reps[name] = v
	pkg.Globals.Set(name, v)
	return nil
}

// sourceReplacementValue validates replacement against the source function
// declaration and normalizes plain Go funcs into script-callable builtins.
func (e *Engine) sourceReplacementValue(pkg *runtime.Package, name string, replacement any) (runtime.Value, error) {
	d, ok := pkg.Index.Funcs[name]
	if !ok || d.Kind != index.FuncDecl || d.Func == nil || d.Func.Type == nil {
		return nil, fmt.Errorf("ReplaceSourceFunc %s.%s: no source function with that name", pkg.Path, name)
	}
	if d.Func.Type.TypeParams != nil && len(d.Func.Type.TypeParams.List) != 0 {
		return nil, fmt.Errorf("ReplaceSourceFunc %s.%s: generic source functions are not supported", pkg.Path, name)
	}
	v := runtime.Unwrap(replacement)
	if fn := replacementFunc(v); fn != nil {
		if len(fn.TParams) != 0 {
			return nil, fmt.Errorf("ReplaceSourceFunc %s.%s: generic replacement functions are not supported", pkg.Path, name)
		}
		req := &runtime.Function{Pkg: pkg, File: d.File, Decl: d.Func, Name: name}
		if !runtime.SigIdentical(req, fn, sourceTypeResolver{e.td}) {
			return nil, fmt.Errorf("ReplaceSourceFunc %s.%s: replacement signature does not match the source function", pkg.Path, name)
		}
		return v, nil
	}

	target := replacement
	if bf, ok := v.(*runtime.BuiltinFunc); ok {
		target = bf.Target
	}
	rt := reflect.TypeOf(target)
	if rt == nil || rt.Kind() != reflect.Func {
		return nil, fmt.Errorf("ReplaceSourceFunc %s.%s: replacement must be a script or Go function, got %T", pkg.Path, name, replacement)
	}
	want := hostbridge.SigHostType(d.Func.Type)
	if rt != want {
		return nil, fmt.Errorf("ReplaceSourceFunc %s.%s: Go replacement type %s does not match source-compatible type %s", pkg.Path, name, rt, want)
	}
	if _, ok := v.(*runtime.BuiltinFunc); ok {
		return v, nil
	}
	return stdlib.WrapBind(pkg.Path+"."+name, target), nil
}

// sourceTypeResolver adapts the typedef resolver to runtime.TypeResolver;
// its interface-signature hook has the typedef package's more specific name.
type sourceTypeResolver struct{ td *typedef.Resolver }

func (r sourceTypeResolver) PeelAlias(td *runtime.TypeDef) *runtime.TypeDef {
	return r.td.PeelAliasTd(td)
}
func (r sourceTypeResolver) IfaceReqs(td *runtime.TypeDef) (map[string]bool, error) {
	return r.td.IfaceReqs(td)
}
func (r sourceTypeResolver) IfaceSigs(td *runtime.TypeDef) (map[string]*runtime.Function, error) {
	return r.td.IfaceSigReqs(td)
}
func (r sourceTypeResolver) ResolveType(td *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	return r.td.ResolveTypeRef(td, x)
}
func (r sourceTypeResolver) ElemOf(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	return r.td.ElemOf(td)
}
func (r sourceTypeResolver) Underlying(td *runtime.TypeDef) *runtime.TypeDef {
	u, err := r.td.Underlying(td)
	if err != nil || u == nil {
		return td
	}
	return u
}

func replacementFunc(v runtime.Value) *runtime.Function {
	switch x := v.(type) {
	case *runtime.Function:
		return x
	case *runtime.Closure:
		return x.Fn
	default:
		return nil
	}
}

// installSourceReplacements seeds registered replacements before indexing
// completes. Revalidating here catches source edits between registration
// and a later session's first load of the same package.
func (e *Engine) installSourceReplacements(p *runtime.Package) error {
	e.mu.Lock()
	reps := make(map[string]runtime.Value, len(e.srcReplacements[p.Path]))
	for name, v := range e.srcReplacements[p.Path] {
		reps[name] = v
	}
	e.mu.Unlock()
	for name, v := range reps {
		if _, err := e.sourceReplacementValue(p, name, v); err != nil {
			return err
		}
		p.Globals.Set(name, v)
	}
	return nil
}
