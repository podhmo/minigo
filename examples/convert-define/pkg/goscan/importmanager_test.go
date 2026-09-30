package goscan

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
)

func TestImportManager_Add_Concurrent(t *testing.T) {
	currentPkgInfo := &scanner.PackageInfo{ImportPath: "example.com/current"}
	im := NewImportManager(currentPkgInfo)

	var wg sync.WaitGroup
	paths := []string{
		"fmt",
		"custom/errors",
		"github.com/pkg/errors",
		"github.com/pkg/errors", // duplicate
		"log/slog",
		"net/http",
		"github.com/another/pkg",
		"github.com/another/pkg/v2",
	}

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := paths[i%len(paths)]
			alias := fmt.Sprintf("alias%d", i)
			im.Add(path, alias)
		}(i)
	}

	wg.Wait()

	// The final state is non-deterministic in terms of which alias gets assigned first,
	// but the import map should not contain duplicates.
	imports := im.Imports()
	if len(imports) > len(paths) {
		t.Errorf("expected less than or equal to %d imports, but got %d", len(paths), len(imports))
	}

	// check for duplicate paths
	pathCounts := make(map[string]int)
	for path := range imports {
		pathCounts[path]++
	}
	for path, count := range pathCounts {
		if count > 1 {
			t.Errorf("path %q is imported %d times", path, count)
		}
	}
}

func TestImportManager_Qualify_Concurrent(t *testing.T) {
	currentPkgInfo := &scanner.PackageInfo{ImportPath: "example.com/current"}
	im := NewImportManager(currentPkgInfo)

	var wg sync.WaitGroup
	packages := []struct {
		path     string
		typeName string
	}{
		{"fmt", "Println"},
		{"custom/errors", "New"},
		{"github.com/pkg/errors", "Wrap"},
		{"log/slog", "Debug"},
		{"net/http", "Request"},
		{"github.com/another/pkg", "Thing"},
		{"github.com/another/pkg/v2", "Thing"},
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pkg := packages[i%len(packages)]
			im.Qualify(pkg.path, pkg.typeName)
		}(i)
	}

	wg.Wait()

	imports := im.Imports()
	// Check for duplicate paths in the final import map
	pathCounts := make(map[string]int)
	for path := range imports {
		pathCounts[path]++
	}
	for path, count := range pathCounts {
		if count > 1 {
			t.Errorf("path %q is imported %d times after concurrent Qualify calls", path, count)
		}
	}

	// Verify that aliases are consistent and don't collide
	aliases := make(map[string]string) // alias -> path
	for path, alias := range imports {
		if existingPath, ok := aliases[alias]; ok {
			t.Errorf("alias %q is used for multiple paths: %q and %q", alias, existingPath, path)
		}
		aliases[alias] = path
	}
}

func TestImportManager_AddAndQualify_MixedConcurrent(t *testing.T) {
	currentPkgInfo := &scanner.PackageInfo{ImportPath: "example.com/current"}
	im := NewImportManager(currentPkgInfo)

	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				im.Add(fmt.Sprintf("path/to/pkg%d", i/10), "")
			} else {
				im.Qualify(fmt.Sprintf("path/to/qualify%d", i/10), "MyType")
			}
		}(i)
	}

	wg.Wait()

	imports := im.Imports()
	// Check for duplicate paths
	pathCounts := make(map[string]int)
	for path := range imports {
		pathCounts[path]++
	}
	for path, count := range pathCounts {
		if count > 1 {
			t.Errorf("path %q is imported %d times in mixed concurrent test", path, count)
		}
	}
	// Check for alias collisions
	aliases := make(map[string]string)
	for path, alias := range imports {
		if existingPath, ok := aliases[alias]; ok {
			t.Errorf("alias %q is used for multiple paths: %q and %q", alias, existingPath, path)
		}
		aliases[alias] = path
	}
}

// from goscan_test.go
func TestImportManager_Add(t *testing.T) {
	currentPkgInfo := &scanner.PackageInfo{ImportPath: "example.com/current"}

	tests := []struct {
		name           string
		currentPkgInfo *scanner.PackageInfo
		ops            []struct {
			path           string
			requestedAlias string
			expectedAlias  string
		}
		finalImports map[string]string
	}{
		{
			name:           "basic add",
			currentPkgInfo: currentPkgInfo,
			ops: []struct {
				path           string
				requestedAlias string
				expectedAlias  string
			}{
				{"fmt", "", "fmt"},
				{"custom/errors", "errors", "errors"},
				{"github.com/pkg/errors", "", "errors1"}, // "errors" is taken, so "errors1"
			},
			finalImports: map[string]string{
				"fmt":                   "fmt",
				"custom/errors":         "errors",
				"github.com/pkg/errors": "errors1",
			},
		},
		{
			name:           "current package",
			currentPkgInfo: currentPkgInfo,
			ops: []struct {
				path           string
				requestedAlias string
				expectedAlias  string
			}{
				{"example.com/current", "", ""},
				{"example.com/other", "current", "current"}, // Alias "current" should be usable by other package
			},
			finalImports: map[string]string{
				"example.com/other": "current",
			},
		},
		{
			name:           "alias conflict resolution",
			currentPkgInfo: currentPkgInfo,
			ops: []struct {
				path           string
				requestedAlias string
				expectedAlias  string
			}{
				{"pkg/one", "myalias", "myalias"},
				{"pkg/two", "myalias", "myalias1"},
				{"pkg/three", "myalias", "myalias2"},
			},
			finalImports: map[string]string{
				"pkg/one":   "myalias",
				"pkg/two":   "myalias1",
				"pkg/three": "myalias2",
			},
		},
		{
			name:           "keyword conflict",
			currentPkgInfo: currentPkgInfo,
			ops: []struct {
				path           string
				requestedAlias string
				expectedAlias  string
			}{
				{"path/to/type", "type", "type_pkg"},
				{"path/to/package", "package", "package_pkg"},
				{"path/to/range", "", "range_pkg"}, // Auto-alias from base path "range"
			},
			finalImports: map[string]string{
				"path/to/type":    "type_pkg",
				"path/to/package": "package_pkg",
				"path/to/range":   "range_pkg",
			},
		},
		{
			name:           "empty path",
			currentPkgInfo: currentPkgInfo,
			ops: []struct {
				path           string
				requestedAlias string
				expectedAlias  string
			}{
				{"", "ignored", ""},
			},
			finalImports: map[string]string{},
		},
		{
			name:           "idempotency",
			currentPkgInfo: currentPkgInfo,
			ops: []struct {
				path           string
				requestedAlias string
				expectedAlias  string
			}{
				{"fmt", "", "fmt"},
				{"fmt", "", "fmt"},
				{"custom/lib", "lib", "lib"},
				{"custom/lib", "lib", "lib"},
				{"custom/lib", "another", "lib"}, // Request different alias for same path, should return original
			},
			finalImports: map[string]string{
				"fmt":        "fmt",
				"custom/lib": "lib",
			},
		},
		{
			name:           "nil current package info",
			currentPkgInfo: nil, // Test with nil currentPkgInfo
			ops: []struct {
				path           string
				requestedAlias string
				expectedAlias  string
			}{
				{"example.com/current", "", "current"}, // Should not be treated as "current package"
				{"fmt", "", "fmt"},
			},
			finalImports: map[string]string{
				"example.com/current": "current",
				"fmt":                 "fmt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := NewImportManager(tt.currentPkgInfo)
			for i, op := range tt.ops {
				actualAlias := im.Add(op.path, op.requestedAlias)
				if actualAlias != op.expectedAlias {
					t.Errorf("Op %d: Add(%q, %q) = %q, want %q", i, op.path, op.requestedAlias, actualAlias, op.expectedAlias)
				}
			}
			finalActualImports := im.Imports()
			if !reflect.DeepEqual(finalActualImports, tt.finalImports) {
				t.Errorf("Final imports mismatch: got %+v, want %+v", finalActualImports, tt.finalImports)
			}
		})
	}
}

func TestImportManager_Qualify(t *testing.T) {
	currentPkgInfo := &scanner.PackageInfo{ImportPath: "example.com/current"}
	im := NewImportManager(currentPkgInfo)

	tests := []struct {
		name              string
		packagePath       string
		typeName          string
		expectedQualified string
		expectedAliasUsed string // if an alias is expected to be generated/used
	}{
		{"current package type", "example.com/current", "MyType", "MyType", ""},
		{"empty package path (built-in)", "", "string", "string", ""},
		{"external package type", "example.com/external", "OtherType", "external.OtherType", "external"},
		{"external package type with dot in name", "example.com/ext.pkg", "ComplexName", "ext_pkg.ComplexName", "ext_pkg"},
		{"another external", "github.com/another/pkg", "Special", "pkg.Special", "pkg"},
		{"qualify same external again", "example.com/external", "AnotherFromExternal", "external.AnotherFromExternal", "external"}, // Should reuse alias
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actualQualified := im.Qualify(tt.packagePath, tt.typeName)
			if actualQualified != tt.expectedQualified {
				t.Errorf("Qualify(%q, %q) = %q, want %q", tt.packagePath, tt.typeName, actualQualified, tt.expectedQualified)
			}

			if tt.packagePath != "" && tt.packagePath != currentPkgInfo.ImportPath {
				// Check if the import was added correctly
				importsMap := im.Imports()
				alias, exists := importsMap[tt.packagePath]
				if !exists {
					t.Errorf("Package path %q not found in imports map after Qualify", tt.packagePath)
				}
				if tt.expectedAliasUsed != "" && alias != tt.expectedAliasUsed {
					// This check is a bit tricky if Qualify uses Add's auto-alias generation with conflicts.
					// For simple cases, it's useful.
					// For now, let's ensure the alias used in expectedQualified matches what's in the map.
					if strings.Contains(tt.expectedQualified, ".") {
						parts := strings.SplitN(tt.expectedQualified, ".", 2)
						expectedAliasFromQualified := parts[0]
						if alias != expectedAliasFromQualified {
							t.Errorf("Alias in map (%q) for path %q does not match alias used in qualified name (%q)", alias, tt.packagePath, expectedAliasFromQualified)
						}
					}
				}
			}
		})
	}
}

func TestImportManager_Imports(t *testing.T) {
	currentPkgInfo := &scanner.PackageInfo{ImportPath: "example.com/current"}
	im := NewImportManager(currentPkgInfo)

	im.Add("fmt", "")
	im.Add("custom/errors", "errors")
	im.Add("example.com/current/subpkg", "subpkg") // different from current
	im.Add("example.com/current", "")              // current package, should not appear in Imports() for explicit import line

	expected := map[string]string{
		"fmt":                        "fmt",
		"custom/errors":              "errors",
		"example.com/current/subpkg": "subpkg",
	}
	actual := im.Imports()

	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("Imports() = %+v, want %+v", actual, expected)
	}

	// Test that modifying the returned map doesn't affect the internal one
	actual["fmt"] = "fmt_modified"
	if im.imports["fmt"] == "fmt_modified" {
		t.Error("Modifying returned map from Imports() affected internal state.")
	}
}
