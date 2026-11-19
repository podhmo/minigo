package minigo

import (
	"go/ast"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// reqKey spells a method name in the requirement/method-set key space:
// exported names are package-free, but an unexported name belongs to
// its declaring package — `interface{ m() }` declared in pkgA is not
// satisfied by pkgB's `m`. The pkg-qualified key rides Names/Depths/
// Funcs alongside the plain name, which member selection keeps using.
func reqKey(pkg *runtime.Package, name string) string {
	if pkg == nil || ast.IsExported(name) {
		return name
	}
	return pkg.Path + "." + name
}

// plainReqName recovers the method name a pkg-qualified requirement
// key encodes (pkg `a`'s `m` is keyed "a.m"); exported keys carry no
// package part and return unchanged. Method names never contain '.',
// so the last dot marks the join.
func plainReqName(key string) string {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// ifaceReqs returns the required method set of an interface typedef:
// declared methods union the requirements of embedded interface elements.
// Constraint elements (~T, unions) are approximated away — satisfaction
// checks treat them as fulfilled.
func (e *Engine) ifaceReqs(td *runtime.TypeDef) (map[string]bool, error) {
	return e.ifaceReqSet(td), nil
}

// ifaceReqSet is ifaceReqs cached on td (callers treat the set as
// read-only). A set built while an embed failed to resolve — a
// constraint element (~T, unions) or a not-yet-loadable package — is
// not cached, so it is recomputed exactly as before.
func (e *Engine) ifaceReqSet(td *runtime.TypeDef) map[string]bool {
	if td == nil {
		return nil
	}
	if set, ok := td.CachedIfaceReqs(); ok {
		return set
	}
	complete := true
	set := e.ifaceReqsRec(td, map[*runtime.TypeDef]bool{}, &complete)
	if complete {
		td.SetIfaceReqs(set)
	}
	return set
}

func (e *Engine) ifaceReqsRec(td *runtime.TypeDef, seen map[*runtime.TypeDef]bool, complete *bool) map[string]bool {
	td = e.peelAliasTd(td)
	if td == nil || seen[td] {
		return nil
	}
	seen[td] = true
	set := map[string]bool{}
	for _, m := range td.MReqs {
		set[reqKey(td.Pkg, m)] = true
	}
	for _, spec := range td.IEmbeds {
		sub, err := e.resolveTypeRef(td, spec)
		if err != nil || sub == nil {
			*complete = false
			continue // constraint exprs (~T, |) don't resolve to typedefs
		}
		for m := range e.ifaceReqsRec(sub, seen, complete) {
			set[m] = true
		}
	}
	return set
}
