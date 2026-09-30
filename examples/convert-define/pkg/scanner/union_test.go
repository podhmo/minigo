package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestScanner_UnionTypeParsing(t *testing.T) {
	// 1. Setup: Define the source code with the union interface and create the test files.
	source := `
package mymodule

type Foo struct{}
type Bar struct{}

// Loginable is an interface with a union of types.
type Loginable interface {
	*Foo | Bar // Note: one pointer, one value type
}
`
	testDir := t.TempDir()
	files := map[string]string{
		"go.mod":  "module mymodule",
		"main.go": source,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(testDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write file %s: %v", name, err)
		}
	}

	// 2. Action: Create a scanner and scan the package.
	s := newTestScanner(t, "mymodule", testDir)
	pkgInfo, err := s.ScanFiles(context.Background(), []string{filepath.Join(testDir, "main.go")}, testDir)
	if err != nil {
		t.Fatalf("ScanFiles failed: %v", err)
	}

	// 3. Assertions: Meticulously validate the parsed structure.
	loginableType := pkgInfo.Lookup("Loginable")
	if loginableType == nil {
		t.Fatal("could not find type Loginable")
	}

	if loginableType.Interface == nil {
		t.Fatal("Loginable is not an interface")
	}

	// The `Union` field should be populated, and `Embedded` should be empty.
	if len(loginableType.Interface.Embedded) != 0 {
		t.Errorf("expected 0 embedded types, but got %d", len(loginableType.Interface.Embedded))
	}
	if len(loginableType.Interface.Union) != 2 {
		t.Fatalf("expected interface to have 2 union members, but got %d", len(loginableType.Interface.Union))
	}

	// Validate each member of the union.
	expectedUnionMembers := map[string]struct {
		IsPointer bool
		PkgName   string
	}{
		"Foo": {IsPointer: true, PkgName: ""}, // PkgName is empty for local types.
		"Bar": {IsPointer: false, PkgName: ""},
	}

	for _, member := range loginableType.Interface.Union {
		var name string
		var isPointer bool

		if member.IsPointer {
			isPointer = true
			if member.Elem != nil {
				name = member.Elem.Name
			} else {
				t.Errorf("union member is a pointer but Elem is nil")
				continue
			}
		} else {
			name = member.Name
		}

		expected, ok := expectedUnionMembers[name]
		if !ok {
			t.Errorf("unexpected union member found: %s", name)
			continue
		}

		if diff := cmp.Diff(expected.IsPointer, isPointer); diff != "" {
			t.Errorf("IsPointer mismatch for member %s (-want +got):\n%s", name, diff)
		}
		if diff := cmp.Diff(expected.PkgName, member.PkgName); diff != "" {
			t.Errorf("PkgName mismatch for member %s (-want +got):\n%s", name, diff)
		}

		delete(expectedUnionMembers, name) // Mark as checked.
	}

	if len(expectedUnionMembers) > 0 {
		for name := range expectedUnionMembers {
			t.Errorf("expected union member %s was not found", name)
		}
	}
}
