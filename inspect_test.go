package minigo_test

import (
	"testing"
)

// TestInspect exercises the minigo.dev/inspect intrinsics through a real
// script (testdata/inspectuse) against a source package
// (testdata/inspectpkg) and the bound stdlib.
func TestInspect(t *testing.T) {
	e := newEngine(t)
	for _, fn := range []string{
		"PkgAccess",
		"DeclsList",
		"SymbolView",
		"FieldsWalk",
		"MethodsWalk",
		"SignatureWalk",
		"TypeExprNav",
		"OriginNav",
		"OwnerChain",
		"HostSignature",
		"CurrentPkg",
		"ImportsList",
		"ValueLayer",
		"BoundDecls",
	} {
		if got := run(t, e, "./testdata/inspectuse", fn); got != "ok" {
			t.Errorf("%s: %v", fn, got)
		}
	}
}
