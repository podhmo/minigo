package minigo

// The lazy-install seam for bound packages: per-path installers
// register into e.pending at engine/session construction (P2-12), and
// the read paths — hostTwin, imports, Bound* queries, the vet check —
// run a path's installer on first use through Install.

import (
	"go/ast"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// hostPackageOf is the typedef lookup's install entry: a registered
// path's install is guaranteed before it is returned; an unregistered
// path only answers the packages already present.
func (e *Engine) hostPackageOf(path string) *runtime.Package {
	e.installBound(path)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pkgs[path]
}

// installBound installs a registered path's bound package on first use.
// A source-built package already in e.pkgs keeps answering loads: the
// bound table still installs into names, but preservePkgs keeps the
// installer's Bind from touching e.pkgs — no overwrite, no restore
// window, so a concurrent Bind can never be clobbered.
func (e *Engine) installBound(path string) {
	e.mu.Lock()
	_, occupied := e.pkgs[path]
	if occupied && e.names.Bound(path) {
		occupied = false // pkgs already holds a bound package
	}
	if occupied {
		if e.preservePkgs == nil {
			e.preservePkgs = map[string]bool{}
		}
		e.preservePkgs[path] = true
	}
	e.mu.Unlock()
	if occupied {
		defer func() {
			e.mu.Lock()
			delete(e.preservePkgs, path)
			e.mu.Unlock()
		}()
	}
	e.pending.Install(path)
}

// bindBound is [Engine.Bind] for the lazy installers: a path already
// bound — an explicit Bind, or a sibling alias installed on first use
// — is not clobbered by the default table. The publish-time check and
// the publish share one critical section, so an explicit Bind landing
// while the table was still building also wins.
func (e *Engine) bindBound(importPath string, symbols map[string]runtime.Value) {
	e.mu.Lock()
	bound := e.names.Bound(importPath)
	e.mu.Unlock()
	if bound {
		return
	}
	p := e.bindTable(importPath, symbols)
	e.mu.Lock()
	if e.names.Bound(importPath) {
		e.mu.Unlock()
		return
	}
	tdKeys := e.bindPublishLocked(importPath, p)
	e.mu.Unlock()
	e.bindInternTypedefs(p, tdKeys)
}

// registered reports whether path is bound — installed already or
// registered for lazy install.
func (e *Engine) registered(path string) bool {
	return e.names.Bound(path) || e.pending.Registered(path)
}

// templateFuncMap returns this session's FuncMap typedef — one per
// registry so td.Pkg names the session's own bound package instead of
// leaking the first engine's across all of them (P2-12).
func (e *Engine) templateFuncMap() *runtime.TypeDef {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sessionState.templateFuncMap == nil {
		e.sessionState.templateFuncMap = &runtime.TypeDef{Name: "text/template.FuncMap", Kind: runtime.KindMap,
			Anon: &ast.MapType{Key: ast.NewIdent("string"), Value: ast.NewIdent("any")}}
	}
	return e.sessionState.templateFuncMap
}

// hostSymbol converts a plain Go value in a [Engine.Bind] symbol map to its
// script form; runtime values pass through unchanged.
func hostSymbol(name string, v runtime.Value) runtime.Value {
	switch v.(type) {
	case *runtime.BuiltinFunc, *runtime.TypeDef, *runtime.GoValue, int64, float64, string, bool:
		return v // the common cases, without reflect
	case nil:
		return runtime.NIL
	}
	t := reflect.TypeOf(v)
	base := t
	if base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	if base.PkgPath() == runtimePkgPath {
		return v
	}
	if t.Kind() == reflect.Func {
		return WrapFunc(name, v)
	}
	if t.Name() != "" && t.PkgPath() != "" && (t.Kind() <= reflect.Complex128 || t.Kind() == reflect.String) {
		return v // named host scalars (time.Duration) are script values as is
	}
	return ValueOf(v)
}
