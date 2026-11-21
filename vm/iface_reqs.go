package vm

import (
	"go/ast"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// selPkg is the package a member selector was compiled in — an
// unexported selector names (pkg, name), not a bare name (M4-2b).
func selPkg(f *frame) *runtime.Package {
	if f != nil && f.fn != nil {
		return f.fn.Pkg
	}
	return nil
}

// selVisible reports whether a member `name` declared in declPkg is
// the one a selector written in selPkg names. Go's identifier rule:
// an unexported name belongs to its declaring package — a selector
// from another package is a different identifier and does not see it.
// A nil side keeps the permissive name-only rule (synthesized typedefs,
// callers with no declaring package).
func selVisible(selPkg, declPkg *runtime.Package, name string) bool {
	if ast.IsExported(name) || selPkg == nil || declPkg == nil {
		return true
	}
	return selPkg == declPkg || selPkg.Path == declPkg.Path
}

// selKey is the key a selector `name` written in selPkg resolves to in
// the method-set/requirement key space — reqKey's twin on the
// selection side: "<pkg path>.<name>" for an unexported name, the bare
// name otherwise.
func selKey(selPkg *runtime.Package, name string) string {
	if selPkg == nil || ast.IsExported(name) {
		return name
	}
	return selPkg.Path + "." + name
}

// selHas is selGet for plain bool sets — requirement sets.
func selHas(set map[string]bool, key string) bool {
	_, ok := selGet(set, key)
	return ok
}

// selGet looks key up in a set keyed the way method sets and
// requirement sets are (reqKey). A bare key — a selector whose call
// site has no declaring package — additionally probes pkg-qualified
// entries by `.<name>` suffix, preserving the old name-only
// reachability for non-script callers.
func selGet[V any](set map[string]V, key string) (V, bool) {
	if m, ok := set[key]; ok {
		return m, true
	}
	if strings.LastIndexByte(key, '.') < 0 {
		suffix := "." + key
		for k, m := range set {
			if strings.HasSuffix(k, suffix) {
				return m, true
			}
		}
	}
	var zero V
	return zero, false
}
