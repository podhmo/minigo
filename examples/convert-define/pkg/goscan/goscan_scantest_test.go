package goscan_test

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	goscan "github.com/podhmo/minigo/examples/convert-define/pkg/goscan"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scantest"
)

// findType is a helper to find a type by name in PackageInfo.
func findType(types []*scanner.TypeInfo, name string) *scanner.TypeInfo {
	for _, ti := range types {
		if ti.Name == name {
			return ti
		}
	}
	return nil
}

func TestImplements_DerivingJSON_Scenario_WithScantest(t *testing.T) {
	files := map[string]string{
		"go.mod": `
module example.com/derivingjson_scenario
go 1.22.4
`,
		"types.go": `
package derivingjson_scenario

type EventData interface {
	EventData()
}

type UserCreated struct{}
func (e *UserCreated) EventData() {}

type MessagePosted struct{}
func (e *MessagePosted) EventData() {}

type NotAnImplementer struct{}
`,
	}

	dir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	action := func(ctx context.Context, s *goscan.Scanner, pkgs []*goscan.Package) error {
		if len(pkgs) != 1 {
			return fmt.Errorf("expected 1 package, but got %d", len(pkgs))
		}
		pkgInfo := pkgs[0]

		// Find the interface
		eventDataInterface := findType(pkgInfo.Types, "EventData")
		if eventDataInterface == nil {
			t.Fatal("Interface 'EventData' not found")
		}

		// Find implementers
		var implementers []*scanner.TypeInfo
		for _, ti := range pkgInfo.Types {
			if ti.Kind == scanner.StructKind {
				if s.Implements(ctx, ti, eventDataInterface) {
					implementers = append(implementers, ti)
				}
			}
		}

		if len(implementers) != 2 {
			t.Fatalf("Expected to find 2 implementers, got %d", len(implementers))
		}

		implementerNames := make([]string, len(implementers))
		for i, impl := range implementers {
			implementerNames[i] = impl.Name
		}
		sort.Strings(implementerNames)

		expectedImplementers := []string{"MessagePosted", "UserCreated"}
		if diff := cmp.Diff(expectedImplementers, implementerNames); diff != "" {
			t.Errorf("mismatch (-want +got):\n%s", diff)
		}
		return nil
	}

	if _, err := scantest.Run(t, context.Background(), dir, []string{"."}, action); err != nil {
		t.Fatalf("scantest.Run() failed: %v", err)
	}
}

func TestRunWithFileSystemPatterns(t *testing.T) {
	files := map[string]string{
		"go.mod": `
module example.com/fs_test
go 1.22.4
`,
		"main.go":    `package main`,
		"pkg/a/a.go": `package a`,
		"pkg/b/b.go": `package b`,
	}

	dir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	action := func(ctx context.Context, s *goscan.Scanner, pkgs []*goscan.Package) error {
		if len(pkgs) != 3 {
			return fmt.Errorf("expected 3 packages, but got %d", len(pkgs))
		}

		gotPkgs := make([]string, len(pkgs))
		for i, p := range pkgs {
			gotPkgs[i] = p.ImportPath
		}
		sort.Strings(gotPkgs)

		wantPkgs := []string{
			"example.com/fs_test",
			"example.com/fs_test/pkg/a",
			"example.com/fs_test/pkg/b",
		}
		if diff := cmp.Diff(wantPkgs, gotPkgs); diff != "" {
			return fmt.Errorf("mismatch (-want +got):\n%s", diff)
		}
		return nil
	}

	if _, err := scantest.Run(t, context.Background(), dir, []string{"./..."}, action); err != nil {
		t.Fatalf("scantest.Run() failed: %v", err)
	}
}
