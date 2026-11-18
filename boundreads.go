package minigo

// Bound-package queries: each routes through the install entry first
// so a registered-but-unbuilt package answers from its lazy table.

import (
	"slices"

	"github.com/podhmo/minigo/runtime"
)

// BoundPackages returns the sorted import paths of every host-bound
// package: std intrinsics and packages registered with [Engine.Bind],
// installed or still pending.
func (e *Engine) BoundPackages() []string {
	e.mu.Lock()
	paths := e.names.Paths()
	e.mu.Unlock()
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		seen[p] = true
	}
	for _, p := range e.pending.Paths() {
		if !seen[p] {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return paths
}

// BoundOf returns the host-bound package registered for path, or nil when
// path is not bound. Unlike [Engine.Package] and [Engine.SourceOf] it
// never loads source, and it ignores [PackageMode]: a [ModeDeny] path
// still reports its registration.
func (e *Engine) BoundOf(path string) *runtime.Package {
	e.installBound(path)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.names.Lookup(path)
}

// BoundSymbols returns the sorted symbol names a bound package exposes to
// scripts, after [WithHostPolicy] filtering; ok is false when importPath
// is not bound.
func (e *Engine) BoundSymbols(importPath string) (names []string, ok bool) {
	e.installBound(importPath)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.names.Symbols(importPath)
}

// registeredVetSymbol reports whether path.Name is already intercepted:
// a special form the compiler emits OpSpecialCall for, or a symbol in a
// host-bound package.
func (e *Engine) registeredVetSymbol(path, name string) bool {
	e.installBound(path)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.specials.Has(runtime.SymbolID{PackagePath: path, Name: name}) {
		return true
	}
	if b := e.names.Lookup(path); b != nil {
		if _, ok := b.Globals.Get(name); ok {
			return true
		}
	}
	return false
}
