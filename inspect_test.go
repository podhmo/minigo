package minigo_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/podhmo/minigo"
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
		"DocTrim",
		"FieldsWalk",
		"MethodsWalk",
		"MethodsPkg",
		"MethodsFile",
		"MethodsDup",
		"SignatureWalk",
		"SigArity",
		"TypeExprNav",
		"OriginNav",
		"OwnerChain",
		"HostSignature",
		"CurrentPkg",
		"ImportsList",
		"CommentsWalk",
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
		"TypeParamMark",
		"ViewTypeAsserts",
		"RuntimePackageIdentity",
		"RuntimePackageTag",
		"TypeOfNamed",
		"BoundTypeSym",
		"HostMethodSym",
		"SourceOfStruct",
		"EnumWalk",
		"EnumScalars",
		"AliasWalk",
		"VarValueRead", // flips the package State to "ready"
		"InitializerView",
		"PkgMetaView", // metadata via accessors; member shadow wins (#26)
		"BuiltinPathConst",
		"ASTWalk",
		"BodyWalk",
		"BodyNil",
		"BodyScope",
		"BodyLocalDecls",
		"BodyBuiltins",
		"BodyImportName",
		// lenient locators: FileOf/PackageOf build like DirOf — a target
		// that cannot compile still enumerates its decls.
		"FileOfDup",
		"PackageOfDup",
		"LookupSymbolMiss",
		// soft loaders: nil on "names nothing", error on "found but broken"
		"LookupDirOfMiss",
		"LookupPackageOfMiss",
		"LookupSourceOfMiss",
		"LookupFileOfMiss",
		"BoundOfHit",
		"BoundOfMiss",
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
		"InitializerFuncTrap",        // Initializer is a value-spec view
		"InitializerTypeTrap",        // a type decl has no initializer
		"TypeFieldsIdentTrap",        // a named leaf is not a composite
		"MethodSetFuncTrap",          // the method set is a type view
		"ResolveBoundTrap",           // the resolver cannot descend into a bound pkg
		"MissingSymTrap",             // unknown symbol name
		"LookupSymbolArgTrap",        // LookupSymbol keeps the arg contract
		"LookupDirOfEmptyTrap",       // dir exists but holds no Go files -> error
		"LookupPackageOfEmptyTrap",   // path resolves to an unbuildable dir -> error
		"LookupSourceOfEmptyTrap",    // source resolves to an unbuildable dir -> error
		"LookupFileOfBrokenTrap",     // existing path that is no Go file -> error
		"BoundFieldTrap",             // bound type has no decl for Fields
		"BoundMethodTrap",            // bound type has no index for Methods
		"HostSigTrap",                // intrinsic without Target has no signature
		"ImportRefTrap",              // import refs stay namespace-strict
		"PkgUnknownTrap",             // neither member nor field -> undefined
		"PkgUnexportedTrap",          // unexported names trap
		"PkgDirTrap",                 // metadata field names trap with an inspect.* hint
		"CurPkgPathTrap",             // the reported d.Package.Path shape stays loud
		"ASTBoundTrap",               // bound symbols carry no AST node
		"BodyTypeTrap",               // a type decl has no body
		"BodyBoundTrap",              // bound symbols carry no body
		"PackagesMissTrap",           // a missing root is an error — no Lookup twin
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
	// a lazy import that fails to load must surface the load error —
	// never the "expects a type decl" argument-shape error
	if _, err := e.Run(context.Background(), "./testdata/badimport", "LoadErr"); err == nil {
		t.Error("LoadErr: expected the import's load error")
	} else if strings.Contains(err.Error(), "expects") {
		t.Errorf("LoadErr: load error masked as argument error: %v", err)
	}
}

// TestInspectPackages exercises inspect.Packages over a tempdir repo
// whose nested module must claim its own subtree — the enumeration
// shape the test-detect plan asks for.
func TestInspectPackages(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":             "module example.com/root\n\ngo 1.24\n",
		"root.go":            "package root\n\nfunc Root() {}\n",
		"root_test.go":       "package root\n",
		"sub/sub.go":         "package sub\n\nfunc Sub() {}\n",
		"onlytest/x_test.go": "package onlytest\n\nimport \"strings\"\n\nvar _ = strings.NewReader\n",
		"nested/go.mod":      "module example.com/nested\n\ngo 1.24\n",
		"nested/n.go":        "package nested\n\nfunc N() {}\n",
	}
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	e := newEngine(t)
	if got := run(t, e, "./testdata/inspectuse", "PackagesWalk", dir); got != "ok" {
		t.Errorf("PackagesWalk: %v", got)
	}
}

// A resolved-but-broken module graph is "found but broken", not a miss:
// cyclic replaces surface the config error instead of LookupPackageOf
// answering nil.
func TestLookupPackageOfBrokenGraph(t *testing.T) {
	dir := t.TempDir()
	gomod := `module cycmod

go 1.24

require example.com/a v0.0.0
require example.com/b v0.0.0

replace example.com/a => example.com/b v0.0.0
replace example.com/b => example.com/a v0.0.0
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0644); err != nil {
		t.Fatal(err)
	}
	script := `package main

import "github.com/podhmo/minigo/inspect"

func LookupCyclic() string {
	inspect.LookupPackageOf("example.com/a")
	return "ok"
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	e := minigo.NewEngine(dir)
	_, err := e.Run(context.Background(), dir, "LookupCyclic")
	if err == nil {
		t.Fatal("LookupCyclic: want the broken-graph error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("LookupCyclic: want the replace-cycle error, got %v", err)
	}
}
