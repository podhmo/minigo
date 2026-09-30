package scanner_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	goscan "github.com/podhmo/minigo/examples/convert-define/pkg/goscan"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scantest"
)

func TestScanner_FunctionTypeAlias(t *testing.T) {
	source := `
package main

type MyFunc func(s string) error
`
	workdir, cleanup := scantest.WriteFiles(t, map[string]string{
		"go.mod":  "module example.com/main",
		"main.go": source,
	})
	defer cleanup()

	s, err := goscan.New(
		goscan.WithWorkDir(workdir),
		goscan.WithGoModuleResolver(),
	)
	if err != nil {
		t.Fatalf("goscan.New() failed: %v", err)
	}

	pkgs, err := s.Scan(context.Background(), "./...")
	if err != nil {
		t.Fatalf("Scan() failed: %v", err)
	}

	if len(pkgs) != 1 {
		t.Fatalf("expected 1 package, got %d", len(pkgs))
	}
	mainPkg := pkgs[0]

	myFuncType := mainPkg.Lookup("MyFunc")
	if myFuncType == nil {
		t.Fatal("TypeInfo for MyFunc not found")
	}

	if myFuncType.Kind != scanner.FuncKind {
		t.Errorf("expected kind to be FuncKind, got %v", myFuncType.Kind)
	}

	if myFuncType.Func == nil {
		t.Fatal("myFuncType.Func is nil")
	}

	// This is the core of the bug. The name and package path of the alias
	// were not being propagated to the underlying FunctionInfo.
	wantFuncName := "MyFunc"
	if diff := cmp.Diff(wantFuncName, myFuncType.Func.Name); diff != "" {
		t.Errorf("FunctionInfo.Name mismatch (-want +got):\n%s", diff)
	}

	wantPkgPath := "example.com/main"
	if diff := cmp.Diff(wantPkgPath, myFuncType.Func.PkgPath); diff != "" {
		t.Errorf("FunctionInfo.PkgPath mismatch (-want +got):\n%s", diff)
	}
}
