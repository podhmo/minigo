// explore.go holds the recursive half of scanning: following named type
// references out of a declaration — through pointers, slices, maps,
// chans, func signatures, and generic instantiation arguments — and
// resolving them to declarations in other packages. Resolution is
// scope-gated (the scanned package's subtree only), lazily loaded, and
// cached, so a shared dependency is read exactly once no matter how many
// fields point at it, and cyclic structures terminate on a visited set.
package scanx

import (
	"strings"

	"github.com/podhmo/minigo/inspect"
)

// TypeRefName reports the canonical "import/path.Name" of the type a
// type expression names, or "" when it names nothing resolvable — a
// builtin, a composite, or a selector that is not a package qualifier.
// SymbolID collapses local aliases through the declaring file's import
// table, so the same type read through two files reports the same id.
func TypeRefName(te *inspect.TypeExpr) string {
	if te == nil || (te.Kind != "Ident" && te.Kind != "SelectorExpr" && !strings.HasPrefix(te.Kind, "reflect:")) {
		return ""
	}
	sid := inspect.SymbolID(te)
	if sid == nil {
		return "" // names nothing — a composite or unqualified selector
	}
	if sid.PackagePath == inspect.BuiltinPackagePath {
		return "" // a builtin names no declaration
	}
	return sid.PackagePath + "." + sid.Name
}

// TypeRefs collects the named type references inside a type expression,
// as their canonical "import/path.Name" names: every named leaf found
// by walking composite children (ptr, slice, array, map, chan, func
// params and results, generic instantiation bases and arguments,
// interface method types). A named leaf is not descended into — its
// own children, if any, belong to the resolved decl, not to this
// spelling.
func TypeRefs(te *inspect.TypeExpr) []string {
	out := []string{}
	var walk func(t *inspect.TypeExpr)
	walk = func(t *inspect.TypeExpr) {
		if t == nil {
			return
		}
		if name := TypeRefName(t); name != "" {
			out = append(out, name)
			return
		}
		for _, c := range inspect.Children(t) {
			walk(c)
		}
	}
	walk(te)
	return out
}

// SplitTypeRef splits a canonical "import/path.Name" into its package
// path and type name — the last "." separates the name even when the
// package path itself contains dots.
func SplitTypeRef(cn string) (pkgPath, name string) {
	i := strings.LastIndex(cn, ".")
	if i < 0 {
		return "", cn
	}
	return cn[:i], cn[i+1:]
}

// Explorer is a scope-gated, lazy, cached view of the declarations under
// one package subtree: a name resolves only when its package path is the
// scope root or beneath it, each package's decl table is built at most
// once however many references point at it, and misses — type
// parameters, names that don't exist — are cached as such.
type Explorer struct {
	scope string
	decls map[string]map[string]*inspect.Decl
}

// NewExplorer scopes name resolution to scope and packages beneath it.
func NewExplorer(scope string) *Explorer {
	return &Explorer{scope: scope, decls: map[string]map[string]*inspect.Decl{}}
}

// Scope returns the root package path this explorer resolves under.
func (e *Explorer) Scope() string { return e.scope }

// InScope reports whether a canonical type name (or package path) is
// resolvable by this explorer: inside the scope package itself or in a
// package beneath it.
func (e *Explorer) InScope(name string) bool {
	return strings.HasPrefix(name, e.scope+".") || strings.HasPrefix(name, e.scope+"/")
}

// Lookup resolves a canonical "import/path.Name" to its decl, or nil
// when it leaves the subtree or no decl of that name exists. Packages
// are loaded on first touch and negative results are cached, so each
// package is read exactly once per Explorer.
func (e *Explorer) Lookup(cn string) *inspect.Decl {
	if !e.InScope(cn) {
		return nil
	}
	pkgPath, name := SplitTypeRef(cn)
	m, ok := e.decls[pkgPath]
	if !ok {
		m = map[string]*inspect.Decl{}
		e.decls[pkgPath] = m
		// SourceOf, not PackageOf: a bound path answers PackageOf with
		// the host package whose index is empty — decls there can't be
		// introspected. SourceOf builds the source index behind the
		// bound shadow (and falls back to PackageOf when unbound).
		if p := inspect.SourceOf(pkgPath); p != nil {
			// A file whose package clause differs from the package's
			// own is foreign — go build rejects the directory — so its
			// decls must not resolve (the same skip rule the script
			// applies to the scanned package's own files).
			pkgName := inspect.Name(p)
			for _, f := range inspect.Files(p) {
				if f.PkgName != "" && f.PkgName != pkgName {
					continue
				}
				for _, d := range inspect.Decls(f) {
					m[d.Name] = d
				}
			}
		}
	}
	return m[name]
}

// Reach breadth-first walks the named types reachable from root's
// definition — a struct's field types, an interface's method types, an
// alias's target — visiting each in-scope type decl at most once. The
// root itself is never visited. Cyclic graphs terminate because decls
// are keyed by canonical name before enqueueing. visit returning false
// stops the walk early.
func (e *Explorer) Reach(root *inspect.Decl, visit func(d *inspect.Decl) bool) {
	seen := map[string]bool{}
	queue := []*inspect.Decl{root}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		def := inspect.Def(d)
		if def == nil {
			continue
		}
		for _, cn := range TypeRefs(def) {
			if seen[cn] {
				continue
			}
			seen[cn] = true
			nd := e.Lookup(cn)
			if nd == nil || inspect.Kind(nd) != "type" {
				continue
			}
			if !visit(nd) {
				return
			}
			queue = append(queue, nd)
		}
	}
}
