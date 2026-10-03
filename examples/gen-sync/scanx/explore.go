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
// CanonicalName already collapses local aliases through the declaring
// file's import table, so the same type read through two files reports
// the same name.
func TypeRefName(te *inspect.TypeExpr) string {
	if te == nil || (te.Kind != "Ident" && te.Kind != "SelectorExpr" && !strings.HasPrefix(te.Kind, "reflect:")) {
		return ""
	}
	cn := te.CanonicalName()
	// canonical names either carry a package path (contains "/") or are
	// bare predeclared/builtin spellings — package paths without "/" are
	// not distinguished here.
	if !strings.Contains(cn, "/") {
		return ""
	}
	return cn
}

// TypeRefs collects the named type references inside a type expression,
// as their canonical "import/path.Name" names: every named leaf found
// by walking composite children (ptr, slice, array, map, chan, func
// params and results, generic instantiation arguments, interface method
// types). A named leaf is not descended into — its own children, if
// any, belong to the resolved decl, not to this spelling.
//
// Known blind spots: the *base* of a generic instantiation (List in
// List[T]) is not among the children inspect exposes, so generic
// declarations are never reached through instantiations of them; and
// module paths without "/" cannot be told apart from builtins.
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
		if p := inspect.PackageOf(pkgPath); p != nil {
			for _, d := range inspect.Decls(p) {
				m[d.Name] = d
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
