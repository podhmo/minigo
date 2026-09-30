package minigo_test

import (
	"context"
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
		"RecursiveOrigin",
		"ShapeDetails",
		"MethodValueSym",
		"HostPtrOwner",
		"TypeOfType",
		"IfaceMembers",
		"SourceOfSrc",
	} {
		if got := run(t, e, "./testdata/inspectuse", fn); got != "ok" {
			t.Errorf("%s: %v", fn, got)
		}
	}

	// TypeOf on a non-type decl must trap, not return a func value
	if _, err := e.Run(context.Background(), "./testdata/inspectuse", "TypeOfFuncTrap"); err == nil {
		t.Error("TypeOfFuncTrap: expected trap for func decl, got nil")
	}
	// MReqs on a non-interface decl must trap too
	if _, err := e.Run(context.Background(), "./testdata/inspectuse", "MReqsStructTrap"); err == nil {
		t.Error("MReqsStructTrap: expected trap for struct decl, got nil")
	}
}
