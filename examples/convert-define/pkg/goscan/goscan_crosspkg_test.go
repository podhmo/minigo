package goscan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
)

func TestScanner_CrossPackageResolution(t *testing.T) {
	// This test simulates a more complex, cross-package type resolution scenario
	// by creating a temporary file structure.
	// a.A depends on b.B
	// b.B depends on c.C
	// c.C is a basic type.
	// This tests the scanner's ability to "walk" the dependency graph to fully resolve a.A.

	// 1. Create a temporary directory for our test module.
	tempDir := t.TempDir()
	testModuleRoot := tempDir
	testModulePath := "example.com/testmodule"

	// 2. Define the file structure and content.
	files := map[string]string{
		"go.mod": `module ` + testModulePath,
		"a/a.go": `
package a
import "` + testModulePath + `/b"
type A struct {
	B b.B
}`,
		"b/b.go": `
package b
import "` + testModulePath + `/c"
type B struct {
	C c.C
}`,
		"c/c.go": `
package c
type C struct {
	Name string
}`,
	}

	// 3. Write the files to the temporary directory.
	for path, content := range files {
		fullPath := filepath.Join(testModuleRoot, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create directory %s: %v", dir, err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write file %s: %v", fullPath, err)
		}
	}

	// 4. Create a scanner pointing to our temporary module.
	s, err := New(WithWorkDir(testModuleRoot))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// 5. We start by scanning package 'a' to find type 'A'.
	pkgA, err := s.ScanPackageFromImportPath(context.Background(), testModulePath+"/a")
	if err != nil {
		t.Fatalf("ScanPackageFromImportPath('a') failed: %v", err)
	}

	typeA := pkgA.Lookup("A")
	if typeA == nil {
		t.Fatalf("could not find type 'A' in package 'a'")
	}

	// 6. Resolve the 'B' field of type 'A'.
	if len(typeA.Struct.Fields) != 1 {
		t.Fatalf("expected 1 field in type A, got %d", len(typeA.Struct.Fields))
	}
	fieldB := typeA.Struct.Fields[0]

	resolvedFieldB, err := s.ResolveType(context.Background(), fieldB.Type)
	if err != nil {
		t.Fatalf("ResolveType for field 'B' failed: %v", err)
	}

	// 7. Verify the resolved type B.
	if resolvedFieldB.Name != "B" {
		t.Errorf("expected resolved field to be 'B', got %q", resolvedFieldB.Name)
	}
	if resolvedFieldB.PkgPath != testModulePath+"/b" {
		t.Errorf("expected resolved field package to be %q, got %q", testModulePath+"/b", resolvedFieldB.PkgPath)
	}

	// 8. Go one level deeper and resolve the 'C' field within 'B'.
	if len(resolvedFieldB.Struct.Fields) != 1 {
		t.Fatalf("expected 1 field in type B, got %d", len(resolvedFieldB.Struct.Fields))
	}
	fieldC := resolvedFieldB.Struct.Fields[0]

	resolvedFieldC, err := s.ResolveType(context.Background(), fieldC.Type)
	if err != nil {
		t.Fatalf("ResolveType for field 'C' failed: %v", err)
	}

	// 9. Final verification of type C.
	if resolvedFieldC.Name != "C" {
		t.Errorf("expected final resolved type to be 'C', got %q", resolvedFieldC.Name)
	}
	if resolvedFieldC.PkgPath != testModulePath+"/c" {
		t.Errorf("expected final resolved package to be %q, got %q", testModulePath+"/c", resolvedFieldC.PkgPath)
	}

	// Check the fields of C itself
	expectedFields := []*scanner.FieldInfo{
		{Name: "Name", Type: &scanner.FieldType{Name: "string", IsBuiltin: true}, IsExported: true},
	}
	opts := []cmp.Option{
		cmpopts.IgnoreFields(scanner.FieldType{}, "Resolver", "CurrentPkg"),
	}
	if diff := cmp.Diff(expectedFields, resolvedFieldC.Struct.Fields, opts...); diff != "" {
		t.Errorf("field mismatch in type C (-want +got):\n%s", diff)
	}
}
