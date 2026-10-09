package locator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"golang.org/x/mod/module"
)

// setupTestModuleWithContent creates a temporary module structure for testing.
// It writes the given goModContent to a go.mod file in the root of the temporary module.
// It also creates any specified subdirectories within the module.
// Returns the root directory of the module, a path to start lookup from (a sub-directory or root), and a cleanup function.
func setupTestModuleWithContent(t *testing.T, goModContent string, subDirsToCreate []string) (string, string, func()) {
	t.Helper()
	rootDir, err := os.MkdirTemp("", "locator-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	goModPath := filepath.Join(rootDir, "go.mod")
	if err := os.WriteFile(goModPath, []byte(goModContent), 0644); err != nil {
		os.RemoveAll(rootDir) // cleanup before fatal
		t.Fatalf("Failed to write go.mod: %v", err)
	}

	for _, p := range subDirsToCreate {
		fullPath := filepath.Join(rootDir, p)
		if err := os.MkdirAll(fullPath, 0755); err != nil {
			os.RemoveAll(rootDir) // cleanup before fatal
			t.Fatalf("Failed to create sub dir %s: %v", fullPath, err)
		}
	}

	var startLookupPath string
	if len(subDirsToCreate) > 0 {
		// Use the first created subdirectory for lookup tests, good for testing upward search for go.mod
		startLookupPath = filepath.Join(rootDir, subDirsToCreate[0])
	} else {
		startLookupPath = rootDir
	}

	return rootDir, startLookupPath, func() {
		os.RemoveAll(rootDir)
	}
}

// setupTestModule is a simplified version for compatibility with old tests.
func setupTestModule(t *testing.T, modulePath string) (string, func()) {
	t.Helper()
	// This setup implies "internal/api" is created and used as the start path for New()
	_, startPath, cleanup := setupTestModuleWithContent(t, "module "+modulePath, []string{filepath.Join("internal", "api")})
	return startPath, cleanup // startPath is rootDir/internal/api
}

func TestNew(t *testing.T) {
	t.Run("from_filesystem", func(t *testing.T) {
		moduleName := "example.com/myproject"
		startLookupPath, cleanup := setupTestModule(t, moduleName)
		defer cleanup()

		l, err := New(startLookupPath) // No options
		if err != nil {
			t.Fatalf("New() returned an error: %v", err)
		}

		expectedRootDir, _ := filepath.Abs(filepath.Dir(filepath.Dir(startLookupPath)))
		actualRootDir, _ := filepath.Abs(l.RootDir())

		if actualRootDir != expectedRootDir {
			t.Errorf("Expected root dir %q, got %q", expectedRootDir, actualRootDir)
		}

		if l.ModulePath() != moduleName {
			t.Errorf("Expected module path %q, got %q", moduleName, l.ModulePath())
		}
	})

	t.Run("from_overlay", func(t *testing.T) {
		moduleName := "example.com/fromoverlay"
		overlayContent := "module " + moduleName
		// Setup a dummy module on filesystem, but the overlay should take precedence
		_, startLookupPath, cleanup := setupTestModuleWithContent(t, "module example.com/filesystem", nil)
		defer cleanup()

		overlay := Overlay{
			"go.mod": []byte(overlayContent),
		}

		l, err := New(startLookupPath, WithOverlay(overlay))
		if err != nil {
			t.Fatalf("New() with overlay returned an error: %v", err)
		}

		if l.ModulePath() != moduleName {
			t.Errorf("Expected module path from overlay %q, got %q", moduleName, l.ModulePath())
		}

		// Check that replaces are also parsed from overlay
		overlayWithReplace := Overlay{
			"go.mod": []byte("module example.com/rp\nreplace example.com/a => ./b"),
		}
		l, err = New(startLookupPath, WithOverlay(overlayWithReplace))
		if err != nil {
			t.Fatalf("New() with overlay and replace returned an error: %v", err)
		}
		if len(l.replaces) != 1 || l.replaces[0].OldPath != "example.com/a" {
			t.Errorf("Expected replace directives to be parsed from overlay, got %v", l.replaces)
		}
	})
}

func TestFindPackageDir(t *testing.T) {
	moduleName := "example.com/myproject"
	// startLookupPath will be .../locator-test-XYZ/internal/api
	startLookupPath, cleanup := setupTestModule(t, moduleName)
	defer cleanup()

	// moduleActualRootDir is where go.mod is.
	moduleActualRootDir := filepath.Dir(filepath.Dir(startLookupPath))

	l, err := New(startLookupPath)
	if err != nil {
		t.Fatalf("New() returned an error: %v", err)
	}

	tests := []struct {
		name        string
		importPath  string
		expectedRel string // Relative to moduleActualRootDir
		expectErr   bool
	}{
		{"standard_subpackage", "example.com/myproject/internal/api", filepath.Join("internal", "api"), false},
		{"standard_root", "example.com/myproject", "", false},
		{"other_project", "example.com/otherproject/api", "", true}, // Cannot find other project
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, err := l.FindPackageDir(tt.importPath)

			if (err != nil) != tt.expectErr {
				t.Fatalf("FindPackageDir() error = %v, expectErr %v. Import path: %s", err, tt.expectErr, tt.importPath)
			}

			if !tt.expectErr {
				expectedPath := filepath.Join(moduleActualRootDir, tt.expectedRel)
				absExpected, _ := filepath.Abs(expectedPath)
				absGot, _ := filepath.Abs(dir)
				if absGot != absExpected {
					t.Errorf("Expected path %q, got %q for import %q", absExpected, absGot, tt.importPath)
				}
			}
		})
	}
}

// TestFindPackageDirWithReplace tests the FindPackageDir method with various replace directives.
func TestFindPackageDirWithReplace(t *testing.T) {
	mainModulePath := "example.com/mainmodule"

	// Create a master temporary directory for tests that might use absolute paths for replacement
	masterTmpDir, err := os.MkdirTemp("", "locator-master-test-*")
	if err != nil {
		t.Fatalf("Failed to create master temp dir: %v", err)
	}
	defer os.RemoveAll(masterTmpDir)

	// Prepare an absolute path target for one of the tests
	absReplaceDir := filepath.Join(masterTmpDir, "absreplacement", "another")
	absReplaceSubPkgDir := filepath.Join(absReplaceDir, "pkg")
	if err := os.MkdirAll(absReplaceSubPkgDir, 0755); err != nil {
		t.Fatalf("Failed to create absolute replacement target dir %s: %v", absReplaceSubPkgDir, err)
	}

	testCases := []struct {
		name              string
		goModContent      string
		subDirsToCreate   []string // Relative to module root for the primary module being set up
		importPath        string
		expectedFoundPath string // Expected absolute path, or relative to the setup module's root
		expectErr         bool
	}{
		{
			name: "replace_with_local_relative_path",
			goModContent: `module example.com/mainmodule
go 1.16
replace example.com/replacedmodule => ./local/replacedmodule
`,
			subDirsToCreate:   []string{filepath.Join("local", "replacedmodule", "pkg")},
			importPath:        "example.com/replacedmodule/pkg",
			expectedFoundPath: filepath.Join("local", "replacedmodule", "pkg"), // Relative to module root
			expectErr:         false,
		},
		{
			name: "replace_with_local_path_root",
			goModContent: `module example.com/mainmodule
go 1.16
replace example.com/replacedmodule => ./local/replacedmodule
`,
			subDirsToCreate:   []string{filepath.Join("local", "replacedmodule")},
			importPath:        "example.com/replacedmodule",
			expectedFoundPath: filepath.Join("local", "replacedmodule"),
			expectErr:         false,
		},
		{
			name: "replace_with_local_path_version_on_old",
			goModContent: `module example.com/mainmodule
go 1.16
replace example.com/replacedmodule v1.0.0 => ./local/versionedreplacedmodule
`,
			subDirsToCreate:   []string{filepath.Join("local", "versionedreplacedmodule", "subpkg")},
			importPath:        "example.com/replacedmodule/subpkg",
			expectedFoundPath: filepath.Join("local", "versionedreplacedmodule", "subpkg"),
			expectErr:         false,
		},
		{
			name:              "replace_with_local_absolute_path",
			goModContent:      "module example.com/mainmodule\ngo 1.16\nreplace example.com/another => " + absReplaceDir,
			subDirsToCreate:   []string{}, // No subdirs needed in the main module for this
			importPath:        "example.com/another/pkg",
			expectedFoundPath: absReplaceSubPkgDir, // This is an absolute path
			expectErr:         false,
		},
		{
			name: "replace_module_with_another_module_path_within_same_project",
			goModContent: `module example.com/mainmodule
go 1.16
replace example.com/oldinternal => example.com/mainmodule/newinternal v1.0.0
`,
			subDirsToCreate:   []string{filepath.Join("newinternal", "api")},
			importPath:        "example.com/oldinternal/api",
			expectedFoundPath: filepath.Join("newinternal", "api"), // Relative to module root
			expectErr:         false,
		},
		{
			name: "replace_target_local_path_does_not_exist",
			goModContent: `module example.com/mainmodule
go 1.16
replace example.com/nonexistent => ./does/not/exist
`,
			subDirsToCreate:   []string{},
			importPath:        "example.com/nonexistent/pkg",
			expectedFoundPath: "",
			expectErr:         true,
		},
		{
			name: "no_matching_replace_directive_falls_back_to_module_path",
			goModContent: `module example.com/mainmodule
go 1.16
replace example.com/foo => ./bar
`,
			subDirsToCreate:   []string{"actualpkg"}, // Subdir of mainmodule
			importPath:        "example.com/mainmodule/actualpkg",
			expectedFoundPath: "actualpkg", // Relative to module root
			expectErr:         false,
		},
		{
			name: "replace_in_block_form_finds_alpha",
			goModContent: `module example.com/mainmodule
go 1.16
replace (
	example.com/alpha => ./local/alpha
	example.com/beta v1.0.0 => ./local/betaversioned
)
`,
			subDirsToCreate:   []string{filepath.Join("local", "alpha"), filepath.Join("local", "betaversioned", "sub")},
			importPath:        "example.com/alpha",
			expectedFoundPath: filepath.Join("local", "alpha"),
			expectErr:         false,
		},
		{
			name: "replace_in_block_form_finds_beta_subpackage",
			goModContent: `module example.com/mainmodule
go 1.16
replace (
	example.com/alpha => ./local/alpha
	example.com/beta v1.0.0 => ./local/betaversioned
)
`,
			subDirsToCreate:   []string{filepath.Join("local", "alpha"), filepath.Join("local", "betaversioned", "sub")},
			importPath:        "example.com/beta/sub",
			expectedFoundPath: filepath.Join("local", "betaversioned", "sub"),
			expectErr:         false,
		},
		{
			name: "replace_to_external_module_not_in_current_locator_scope_fails_gracefully",
			goModContent: `module example.com/mainmodule
go 1.16
replace example.com/currenttomodule => example.com/someothermodule v1.0.0
`,
			subDirsToCreate:   []string{},
			importPath:        "example.com/currenttomodule/pkg",
			expectedFoundPath: "",
			expectErr:         true, // Because someothermodule is not mainmodule and locator is scoped
		},
		{
			name: "replace_old_path_is_prefix_of_import_path",
			goModContent: `module example.com/mainmodule
go 1.16
replace example.com/prefixmod => ./local/prefixmod
`,
			subDirsToCreate:   []string{filepath.Join("local", "prefixmod", "sub", "pkg")},
			importPath:        "example.com/prefixmod/sub/pkg",
			expectedFoundPath: filepath.Join("local", "prefixmod", "sub", "pkg"),
			expectErr:         false,
		},
		{
			name: "replace_old_path_is_prefix_of_import_path_targetting_root_of_replacement",
			goModContent: `module example.com/mainmodule
go 1.16
replace example.com/prefixmod => ./local/prefixmod
`,
			subDirsToCreate:   []string{filepath.Join("local", "prefixmod")}, // only root of replacement exists
			importPath:        "example.com/prefixmod",
			expectedFoundPath: filepath.Join("local", "prefixmod"),
			expectErr:         false,
		},
		{
			// A module nested under a replaced parent (the
			// examples/gen-sync fixture's `module .../examples/gen-sync`
			// + `replace github.com/podhmo/minigo => ../../` shape):
			// the main module claims its own subtree even though a
			// shorter-prefix replace points at a tree that also
			// contains it.
			name: "self_replace_on_parent_module_does_not_shadow_main_subtree",
			goModContent: `module example.com/parent/inner
go 1.16
require example.com/parent v0.0.0
replace example.com/parent => ./replaced-parent
`,
			subDirsToCreate: []string{
				"pkg", // real package inside the inner module
				filepath.Join("replaced-parent", "inner", "pkg"), // shadow in the replaced tree
			},
			importPath:        "example.com/parent/inner/pkg",
			expectedFoundPath: "pkg", // Relative to module root
			expectErr:         false,
		},
		{
			// The same self-replace still owns imports outside the
			// inner module's own prefix.
			name: "self_replace_on_parent_module_still_claims_outside_main_prefix",
			goModContent: `module example.com/parent/inner
go 1.16
require example.com/parent v0.0.0
replace example.com/parent => ./replaced-parent
`,
			subDirsToCreate:   []string{filepath.Join("replaced-parent", "other")},
			importPath:        "example.com/parent/other",
			expectedFoundPath: filepath.Join("replaced-parent", "other"), // Relative to module root
			expectErr:         false,
		},
		{
			// Once the main module claims an import, a missing
			// directory is an error — resolution never silently reads
			// the replaced tree's copy of the same path.
			name: "self_replace_missing_dir_in_main_is_error_not_shadow_read",
			goModContent: `module example.com/parent/inner
go 1.16
require example.com/parent v0.0.0
replace example.com/parent => ./replaced-parent
`,
			subDirsToCreate:   []string{filepath.Join("replaced-parent", "inner", "gone")},
			importPath:        "example.com/parent/inner/gone",
			expectedFoundPath: "",
			expectErr:         true,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			// Ensure module path in goModContent is consistent for tests assuming mainModulePath
			var currentGoModContent string
			if strings.HasPrefix(strings.TrimSpace(tt.goModContent), "module ") {
				currentGoModContent = tt.goModContent
			} else {
				currentGoModContent = "module " + mainModulePath + "\n" + tt.goModContent
			}

			moduleRootDir, startLookupPath, cleanup := setupTestModuleWithContent(t, currentGoModContent, tt.subDirsToCreate)
			defer cleanup()

			l, err := New(startLookupPath)
			if err != nil {
				// This might indicate an issue with go.mod parsing in New() if the go.mod was intended to be valid.
				t.Fatalf("Test %q: New() returned an error: %v. go.mod content:\n%s", tt.name, err, currentGoModContent)
			}

			dir, err := l.FindPackageDir(tt.importPath)

			if (err != nil) != tt.expectErr {
				t.Errorf("Test %q: FindPackageDir() error = %v, expectErr %v. Import path: %s", tt.name, err, tt.expectErr, tt.importPath)
			}

			if !tt.expectErr && err == nil { // Only check path if no error was expected and no error occurred
				var expectedPathAbs string
				if filepath.IsAbs(tt.expectedFoundPath) {
					expectedPathAbs = tt.expectedFoundPath
				} else {
					expectedPathAbs = filepath.Join(moduleRootDir, tt.expectedFoundPath)
				}
				// Normalize paths for comparison
				absExpected, _ := filepath.Abs(expectedPathAbs)
				absGot, _ := filepath.Abs(dir)

				if absGot != absExpected {
					t.Errorf("Test %q: For import %q, expected path %q, got %q", tt.name, tt.importPath, absExpected, absGot)
				}
			} else if !tt.expectErr && err != nil {
				t.Errorf("Test %q: For import %q, expected success, but got error: %v", tt.name, tt.importPath, err)
			} else if tt.expectErr && err == nil {
				t.Errorf("Test %q: For import %q, expected error, but got path: %s", tt.name, tt.importPath, dir)
			}
		})
	}
}

// TODO: Add tests for getReplaceDirectives specifically if complex parsing logic needs unit testing.
// For now, its behavior is indirectly tested via TestFindPackageDirWithReplace.

func TestPathToImport(t *testing.T) {
	// 1. Setup parent module structure
	parentDir, err := os.MkdirTemp("", "parent-module-*")
	if err != nil {
		t.Fatalf("Failed to create parent temp dir: %v", err)
	}
	defer os.RemoveAll(parentDir)

	if err := os.WriteFile(filepath.Join(parentDir, "go.mod"), []byte("module example.com/parent\n"), 0644); err != nil {
		t.Fatalf("Failed to write parent go.mod: %v", err)
	}
	dependencyDir := filepath.Join(parentDir, "dependency")
	if err := os.Mkdir(dependencyDir, 0755); err != nil {
		t.Fatalf("Failed to create dependency dir: %v", err)
	}

	// 2. Setup sub-module structure inside parent
	subDir := filepath.Join(parentDir, "sub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create sub dir: %v", err)
	}
	subGoModContent := `
module example.com/parent/sub
go 1.16
replace example.com/parent => ../
`
	if err := os.WriteFile(filepath.Join(subDir, "go.mod"), []byte(subGoModContent), 0644); err != nil {
		t.Fatalf("Failed to write sub go.mod: %v", err)
	}
	subPackageDir := filepath.Join(subDir, "pkg")
	if err := os.Mkdir(subPackageDir, 0755); err != nil {
		t.Fatalf("Failed to create sub package dir: %v", err)
	}

	// 3. Create locator for the sub-module
	l, err := New(subDir)
	if err != nil {
		t.Fatalf("New() failed in sub-directory: %v", err)
	}

	testCases := []struct {
		name      string
		absPath   string
		expected  string
		expectErr bool
	}{
		{
			name:     "path inside sub-module",
			absPath:  subPackageDir,
			expected: "example.com/parent/sub/pkg",
		},
		{
			name:     "path in parent module via replace",
			absPath:  dependencyDir,
			expected: "example.com/parent/dependency",
		},
		{
			name: "path outside of any known module",
			absPath: func() string {
				otherDir, _ := os.MkdirTemp("", "other-*")
				t.Cleanup(func() { os.RemoveAll(otherDir) })
				return otherDir
			}(),
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := l.PathToImport(tc.absPath)
			if (err != nil) != tc.expectErr {
				t.Fatalf("PathToImport() error = %v, wantErr %v", err, tc.expectErr)
			}
			if !tc.expectErr && got != tc.expected {
				t.Errorf("PathToImport() got = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestFindPackageDirWithReplaceParent(t *testing.T) {
	// This test simulates a common scenario in development where a tool (sub-module)
	// wants to use the development version of its dependency (the parent module).
	// Structure:
	// /tmp/root/
	//   go.mod (module example.com/parent)
	//   dependency/
	//     ...
	//   sub/
	//     go.mod (module example.com/parent/sub, replace example.com/parent => ../)
	//     main.go

	// 1. Setup parent module structure
	parentDir, err := os.MkdirTemp("", "parent-module-*")
	if err != nil {
		t.Fatalf("Failed to create parent temp dir: %v", err)
	}
	defer os.RemoveAll(parentDir)

	if err := os.WriteFile(filepath.Join(parentDir, "go.mod"), []byte("module example.com/parent\n"), 0644); err != nil {
		t.Fatalf("Failed to write parent go.mod: %v", err)
	}
	dependencyDir := filepath.Join(parentDir, "dependency")
	if err := os.Mkdir(dependencyDir, 0755); err != nil {
		t.Fatalf("Failed to create dependency dir: %v", err)
	}

	// 2. Setup sub-module structure inside parent
	subDir := filepath.Join(parentDir, "sub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create sub dir: %v", err)
	}
	subGoModContent := `
module example.com/parent/sub
go 1.16
replace example.com/parent => ../
`
	if err := os.WriteFile(filepath.Join(subDir, "go.mod"), []byte(subGoModContent), 0644); err != nil {
		t.Fatalf("Failed to write sub go.mod: %v", err)
	}

	// 3. Test: from within the sub-module, locate a package in the parent module via replace
	l, err := New(subDir)
	if err != nil {
		t.Fatalf("New() failed in sub-directory: %v", err)
	}

	// We are in "sub", trying to find "example.com/parent/dependency"
	// The replace rule should map "example.com/parent" to "../" relative to sub's root, which is `parentDir`.
	// So, "example.com/parent/dependency" becomes "../dependency", which resolves to `parentDir/dependency`.
	importPath := "example.com/parent/dependency"
	foundPath, err := l.FindPackageDir(importPath)
	if err != nil {
		t.Fatalf("FindPackageDir() for %q returned an error: %v", importPath, err)
	}

	expectedPath, _ := filepath.Abs(dependencyDir)
	foundPathAbs, _ := filepath.Abs(foundPath)

	if foundPathAbs != expectedPath {
		t.Errorf("Expected path %q, got %q", expectedPath, foundPathAbs)
	}
}

func TestFindPackageDirWithGoModuleResolver(t *testing.T) {
	// Setup: Create a fake GOMODCACHE
	fakeGoModCache, err := os.MkdirTemp("", "gomodcache-*")
	if err != nil {
		t.Fatalf("failed to create fake gomodcache: %v", err)
	}
	defer os.RemoveAll(fakeGoModCache)

	// Setup: Create a fake external module in the cache
	depPath := "github.com/some/dependency"
	depVersion := "v1.2.3"
	depDir := filepath.Join(fakeGoModCache, depPath+"@"+depVersion, "pkg")
	if err := os.MkdirAll(depDir, 0755); err != nil {
		t.Fatalf("failed to create fake dependency dir: %v", err)
	}

	// Setup: Create another fake external module with uppercase letters
	depPathWithUpper := "github.com/Azure/go-autorest"
	escapedDepPath, _ := module.EscapePath(depPathWithUpper)
	depVersionWithUpper := "v0.1.0"
	depDirWithUpper := filepath.Join(fakeGoModCache, escapedDepPath+"@"+depVersionWithUpper, "autorest")
	if err := os.MkdirAll(depDirWithUpper, 0755); err != nil {
		t.Fatalf("failed to create fake dependency dir: %v", err)
	}

	// Setup: Create a test module that requires the fake dependency
	goModContent := `
module example.com/testproject
go 1.18
require (
	github.com/some/dependency v1.2.3
	github.com/Azure/go-autorest v0.1.0
)
`
	_, startPath, cleanup := setupTestModuleWithContent(t, goModContent, []string{"cmd"})
	defer cleanup()

	originalGoModCache := os.Getenv("GOMODCACHE")
	os.Setenv("GOMODCACHE", fakeGoModCache)
	defer os.Setenv("GOMODCACHE", originalGoModCache)

	testCases := []struct {
		name                 string
		importPath           string
		useResolver          bool
		expectFound          bool
		expectedPathContains string
	}{
		// Standard Library tests
		{"stdlib_found_with_resolver", "fmt", true, true, filepath.Join("src", "fmt")},
		{"stdlib_not_found_without_resolver_heuristic_miss", "net/http", false, false, ""}, // This might pass if old heuristic is kept, but new logic is what we test
		{"stdlib_subpkg_found_with_resolver", "net/http", true, true, filepath.Join("src", "net", "http")},

		// External Dependency tests
		{"external_dep_found_with_resolver", "github.com/some/dependency/pkg", true, true, depDir},
		{"external_dep_not_found_without_resolver", "github.com/some/dependency/pkg", false, false, ""},
		{"external_dep_root_found_with_resolver", "github.com/some/dependency", true, true, filepath.Dir(depDir)},
		{"external_dep_with_upper_found", "github.com/Azure/go-autorest/autorest", true, true, depDirWithUpper},

		// Negative tests
		{"non_existent_dep_not_found", "github.com/non/existent", true, false, ""},
		{"package_in_module_still_found", "example.com/testproject/cmd", true, true, "cmd"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var opts []Option
			if tc.useResolver {
				opts = append(opts, WithGoModuleResolver())
			}

			// The overlay needs to be passed via an option now
			overlay := Overlay{"go.mod": []byte(goModContent)}
			opts = append(opts, WithOverlay(overlay))

			l, err := New(startPath, opts...)
			if err != nil {
				t.Fatalf("New() failed: %v", err)
			}

			foundPath, err := l.FindPackageDir(tc.importPath)

			if tc.expectFound {
				if err != nil {
					t.Errorf("expected to find path for %q, but got error: %v", tc.importPath, err)
					return
				}
				absFoundPath, _ := filepath.Abs(foundPath)
				if !strings.Contains(absFoundPath, tc.expectedPathContains) {
					t.Errorf("expected found path %q to contain %q", absFoundPath, tc.expectedPathContains)
				}
			} else { // expect not found
				if err == nil {
					t.Errorf("expected error for %q, but found path: %q", tc.importPath, foundPath)
				}
			}
		})
	}
}

func TestGetGoModCache(t *testing.T) {
	t.Run("GOMODCACHE_is_set", func(t *testing.T) {
		expected := "/test/gomodcache"
		original := os.Getenv("GOMODCACHE")
		os.Setenv("GOMODCACHE", expected)
		defer os.Setenv("GOMODCACHE", original)

		path, err := getGoModCache()
		if err != nil {
			t.Fatalf("getGoModCache() error = %v", err)
		}
		if path != expected {
			t.Errorf("got %v, want %v", path, expected)
		}
	})

	t.Run("GOPATH_is_set", func(t *testing.T) {
		// `go env` rejects a GOPATH that isn't an absolute path in host
		// terms, so /test/gopath must become C:\test\gopath on Windows.
		gopath, err := filepath.Abs("/test/gopath")
		if err != nil {
			t.Fatal(err)
		}
		originalGOMODCACHE := os.Getenv("GOMODCACHE")
		originalGOPATH := os.Getenv("GOPATH")
		os.Setenv("GOMODCACHE", "") // Unset
		os.Setenv("GOPATH", gopath)
		defer func() {
			os.Setenv("GOMODCACHE", originalGOMODCACHE)
			os.Setenv("GOPATH", originalGOPATH)
		}()

		expected := filepath.Join(gopath, "pkg", "mod")
		path, err := getGoModCache()
		if err != nil {
			t.Fatalf("getGoModCache() error = %v", err)
		}
		if path != expected {
			t.Errorf("got %v, want %v", path, expected)
		}
	})

	// Testing the home directory case is complex as it depends on the user's environment.
	// We'll skip it as the two main cases are covered.
}

func TestResolvePkgPath(t *testing.T) {
	ctx := context.Background()

	// Manually setup a temporary directory to avoid import cycle with scantest
	tmpdir, err := os.MkdirTemp("", "resolve-pkg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpdir)

	files := map[string]string{
		"go.mod": "module my/app\n",
		"main.go": `package main

func main() {}
`,
		"internal/api/handler.go": `package api`,
	}
	for name, content := range files {
		path := filepath.Join(tmpdir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("failed to create parent dir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write file %s: %v", name, err)
		}
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get working directory: %v", err)
	}
	if err := os.Chdir(tmpdir); err != nil {
		t.Fatalf("could not change to tmpdir: %v", err)
	}
	defer os.Chdir(originalWD)

	cases := []struct {
		name      string
		startPath string
		want      string
		wantErr   bool
	}{
		{
			name:      "current directory",
			startPath: ".",
			want:      "my/app",
			wantErr:   false,
		},
		{
			name:      "subdirectory",
			startPath: "./internal/api",
			want:      "my/app/internal/api",
			wantErr:   false,
		},
		{
			name:      "file in subdirectory",
			startPath: "./internal/api/handler.go",
			want:      "my/app/internal/api",
			wantErr:   false,
		},
		{
			name:      "absolute path to subdirectory",
			startPath: filepath.Join(tmpdir, "internal/api"),
			want:      "my/app/internal/api",
			wantErr:   false,
		},
		{
			name:      "package path that does not exist",
			startPath: "github.com/foo/bar",
			want:      "github.com/foo/bar",
			wantErr:   false,
		},
		{
			name:      "relative file path that does not exist",
			startPath: "./nonexistent",
			wantErr:   true,
		},
		{
			name: "path outside module",
			// This test requires creating a directory outside the module.
			// We'll create a sibling directory to tmpdir.
			startPath: func() string {
				parentDir := filepath.Dir(tmpdir)
				outsideDir := filepath.Join(parentDir, "outside_module")
				os.Mkdir(outsideDir, 0755)
				// Defer cleanup of this new directory
				t.Cleanup(func() { os.RemoveAll(outsideDir) })
				return outsideDir
			}(),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolvePkgPath(ctx, tc.startPath)

			if (err != nil) != tc.wantErr {
				t.Fatalf("ResolvePkgPath() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ResolvePkgPath() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFindPackageDirFromVendor pins the GOROOT vendor rule: an importer
// living under GOROOT/src resolves vendored dependencies through the
// ancestor vendor directories first (net/http ->
// $GOROOT/src/vendor/golang.org/x/net/http/httpguts), while an importer
// outside GOROOT never enters the vendor search.
func TestFindPackageDirFromVendor(t *testing.T) {
	goRoot := t.TempDir()
	importerDir := filepath.Join(goRoot, "src", "net", "http")
	vendored := filepath.Join(goRoot, "src", "vendor", "golang.org", "x", "net", "http", "httpguts")
	for _, d := range []string{importerDir, vendored} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	l := &Locator{goRoot: goRoot}

	got, err := l.FindPackageDirFrom(importerDir, "golang.org/x/net/http/httpguts")
	if err != nil {
		t.Fatalf("vendor lookup failed: %v", err)
	}
	if diff := cmp.Diff(vendored, got); diff != "" {
		t.Errorf("vendor dir mismatch (-want +got):\n%s", diff)
	}

	// an importer outside GOROOT skips the vendor search entirely.
	if _, err := l.FindPackageDirFrom(t.TempDir(), "golang.org/x/net/http/httpguts"); err == nil {
		t.Errorf("expected resolution to fail for a non-GOROOT importer")
	}
}
