package minigo_test

import (
	"context"
	"strings"
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
		"MReqsStructEmpty",
		"SourceOfSrc",
		// coverage-gap pass — see the round-3 audit in
		// docs/sketch/plan-package-introspection.md
		"DeclMeta",
		"FieldPos",
		"PkgMeta", // asserts State=="indexed" — keep before VarValueRead
		"CompositeFields",
		"NamedFieldType",
		"Instantiation",
		"AnonFieldWalk",
		"PromotedWalk",
		"ImplementersWalk",
		"TypeParamsList",
		"TypeOfNamed",
		"BoundTypeSym",
		"HostMethodSym",
		"SourceOfStruct",
		"EnumWalk",
		"AliasWalk",
		"VarValueRead", // flips the package State to "ready"
		"PkgMetaView",  // metadata via accessors; member shadow wins (#26)
		"BuiltinPathConst",
	} {
		if got := run(t, e, "./testdata/inspectuse", fn); got != "ok" {
			t.Errorf("%s: %v", fn, got)
		}
	}

	// TypeOf on a non-type decl must trap, not return a func value
	if _, err := e.Run(context.Background(), "./testdata/inspectuse", "TypeOfFuncTrap"); err == nil {
		t.Error("TypeOfFuncTrap: expected trap for func decl, got nil")
	}
	// remaining documented limitations must also trap, not misreport
	for _, fn := range []string{
		"DefVarTrap",       // Def is TypeSpec-only — var/const types unreachable
		"DeclTypeFuncTrap", // DeclType is ValueSpec-only — funcs/types trap
		"DeclTypeTypeTrap",
		"EnumMembersFuncTrap",  // EnumMembers is a type view — funcs trap
		"EnumMembersBoundTrap", // bound types carry no index to walk
		"ImplementersStructTrap",
		"ImplementersConstraintTrap", // constraint interfaces have no implementers
		"IsAliasFuncTrap",            // IsAlias is a type view — funcs trap
		"IsAliasBoundTrap",           // bound types carry no declaration
		"TypeFieldsIdentTrap",        // a named leaf is not a composite
		"MethodSetFuncTrap",          // the method set is a type view
		"ResolveBoundTrap",           // the resolver cannot descend into a bound pkg
		"MissingSymTrap",             // unknown symbol name
		"BoundFieldTrap",             // bound type has no decl for Fields
		"BoundMethodTrap",            // bound type has no index for Methods
		"HostSigTrap",                // intrinsic without Target has no signature
		"ImportRefTrap",              // import refs stay namespace-strict
		"PkgUnknownTrap",             // neither member nor field -> undefined
		"PkgUnexportedTrap",          // unexported names trap
		"PkgDirTrap",                 // metadata field names trap with an inspect.* hint
		"CurPkgPathTrap",             // the reported d.Package.Path shape stays loud
	} {
		if _, err := e.Run(context.Background(), "./testdata/inspectuse", fn); err == nil {
			t.Errorf("%s: expected trap, got nil", fn)
		}
	}
	// metadata misses spell the inspect.* accessor in the trap message
	for fn, want := range map[string]string{
		"PkgDirTrap":     "inspect.Dir(pkg)",
		"CurPkgPathTrap": "inspect.Path(pkg)",
	} {
		_, err := e.Run(context.Background(), "./testdata/inspectuse", fn)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: expected trap mentioning %q, got %v", fn, want, err)
		}
	}
}
