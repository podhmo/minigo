package bind

import (
	"slices"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// Names is the read-side view of an engine's host-bound package
// registry: which import paths are bound and which package and
// symbols each yields. The registry readers — BoundPackages, BoundOf,
// BoundSymbols, the vet bound-symbol check, SourceOf's bound-shadow
// check and session inheritance — go through it instead of touching
// the underlying map, and P2-11's install entry point consults the
// same view.
//
// Names does not synchronize: callers hold the engine's lock, as they
// did for the map it replaces.
type Names struct {
	pkgs map[string]*runtime.Package
}

func NewNames() *Names {
	return &Names{pkgs: make(map[string]*runtime.Package)}
}

// Bound reports whether path is registered.
func (n *Names) Bound(path string) bool {
	_, ok := n.pkgs[path]
	return ok
}

// Lookup returns the bound package for path, or nil when it is not
// bound.
func (n *Names) Lookup(path string) *runtime.Package {
	return n.pkgs[path]
}

// Set registers p under path.
func (n *Names) Set(path string, p *runtime.Package) {
	n.pkgs[path] = p
}

// Pkgs returns the live registry map for consumers that iterate it
// directly (replhooks); callers hold the engine's lock and must not
// write through it.
func (n *Names) Pkgs() map[string]*runtime.Package {
	return n.pkgs
}

// Paths returns the sorted bound import paths.
func (n *Names) Paths() []string {
	paths := make([]string, 0, len(n.pkgs))
	for path := range n.pkgs {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths
}

// Symbols returns the sorted symbol names the bound package exposes to
// scripts (internal __-prefixed entries hidden); ok is false when path
// is not bound.
func (n *Names) Symbols(path string) (names []string, ok bool) {
	p, ok := n.pkgs[path]
	if !ok {
		return nil, false
	}
	for _, name := range p.Globals.Names() {
		if !strings.HasPrefix(name, "__") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, true
}

// Each calls fn for every bound path and package until fn returns
// false. Iteration order is unspecified.
func (n *Names) Each(fn func(path string, p *runtime.Package) bool) {
	for path, p := range n.pkgs {
		if !fn(path, p) {
			return
		}
	}
}
