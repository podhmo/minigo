package goscan

import (
	"context"
	"encoding/json" // Added for manual cache file creation in tests
	"fmt"           // Added for debug printing in tests
	"go/version"
	"os"            // Added for os.MkdirTemp, os.ReadFile, os.Stat
	"path/filepath" // Added for filepath.Join, filepath.Abs
	"reflect"       // Added for reflect.DeepEqual in tests
	"runtime"
	"sort"    // Added for sorting slices in tests
	"strings" // Added for strings.Contains
	"testing"

	// "time" // Removed: No longer used

	// For deep comparison
	// For options like IgnoreUnexported
	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
)

// TestNew_Integration tests the creation of a new Scanner and its underlying locator.
func TestNew_Integration(t *testing.T) {
	s, err := New(WithWorkDir("../scanner")) // Assuming this test runs from project root where ./scanner is a valid sub-package.
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if s.locator == nil {
		t.Fatal("Scanner locator should not be nil")
	}
	if s.scanner == nil {
		t.Fatal("Scanner scanner should not be nil")
	}
	// This assertion depends on the main go.mod of the project.
	// If running tests from within a nested module or if go.mod changes, this might fail.
	// For now, assume "github.com/podhmo/minigo/examples/convert-define" is the main module.
	if s.locator.ModulePath() != "github.com/podhmo/minigo/examples/convert-define" {
		t.Errorf("Expected module path 'github.com/podhmo/minigo/examples/convert-define', got %q", s.locator.ModulePath())
	}
}

// TestLazyResolution_Integration tests the full scanning and lazy resolution process.
// This test uses testdata/multipkg which should have its own go.mod declaring "example.com/multipkg-test"
func TestLazyResolution_Integration(t *testing.T) {
	// Scanner is initialized relative to the "../testdata/multipkg" module.
	s, err := New(WithWorkDir("../testdata/multipkg"))
	if err != nil {
		t.Fatalf("New() for multipkg failed: %v", err)
	}
	if s.locator.ModulePath() != "example.com/multipkg-test" {
		t.Fatalf("Expected module path 'example.com/multipkg-test', got '%s'", s.locator.ModulePath())
	}

	// Scan the 'api' package, which depends on the 'models' package.
	apiImportPath := "example.com/multipkg-test/api"
	pkgInfo, err := s.ScanPackageFromImportPath(context.Background(), apiImportPath)
	if err != nil {
		t.Fatalf("ScanPackageFromImportPath() failed: %v", err)
	}

	// Find the Handler struct
	var handlerStruct *scanner.TypeInfo
	for _, ti := range pkgInfo.Types {
		if ti.Name == "Handler" {
			handlerStruct = ti
			break
		}
	}
	if handlerStruct == nil {
		t.Fatal("Could not find 'Handler' struct in api package")
	}
	if handlerStruct.Struct == nil || len(handlerStruct.Struct.Fields) == 0 {
		t.Fatal("Handler struct has no fields")
	}

	// Find the User field
	userField := handlerStruct.Struct.Fields[0]
	if userField.Name != "User" {
		t.Fatalf("Expected first field to be 'User', got %s", userField.Name)
	}

	// At this point, the 'models' package should not have been scanned yet.
	s.mu.RLock()
	_, found := s.packageCache["example.com/multipkg-test/models"]
	s.mu.RUnlock()
	if found {
		t.Fatal("'models' package should not be in cache before resolving")
	}

	// Trigger lazy resolution
	userDef, err := s.ResolveType(context.Background(), userField.Type)
	if err != nil {
		t.Fatalf("Failed to resolve User field type: %v", err)
	}

	// Now the 'models' package should be in the cache.
	s.mu.RLock()
	_, found = s.packageCache["example.com/multipkg-test/models"]
	s.mu.RUnlock()
	if !found {
		t.Fatal("'models' package should be in cache after resolving")
	}

	// Check the resolved definition
	if userDef.Name != "User" {
		t.Errorf("Expected resolved type name to be 'User', got %q", userDef.Name)
	}
	if userDef.Kind != scanner.StructKind {
		t.Errorf("Expected resolved type kind to be StructKind")
	}
	if len(userDef.Struct.Fields) != 2 {
		t.Errorf("Expected resolved User struct to have 2 fields, got %d", len(userDef.Struct.Fields))
	}
	if userDef.Struct.Fields[0].Name != "ID" || userDef.Struct.Fields[1].Name != "Name" {
		t.Error("Resolved User struct fields are incorrect")
	}
}

func TestScanner_WithSymbolCache(t *testing.T) {
	// Define import paths from testdata/multipkg
	apiImportPath := "example.com/multipkg-test/api"       // Contains Handler type
	modelsImportPath := "example.com/multipkg-test/models" // Contains User type

	// Initialize scanner relative to the multipkg module
	sRoot, err := New(WithWorkDir("../testdata/multipkg"))
	if err != nil {
		t.Fatalf("Failed to create scanner for multipkg module: %v", err)
	}
	moduleRootDir := sRoot.locator.RootDir() // This will be testdata/multipkg

	expectedHandlerFilePath, _ := filepath.Abs(filepath.Join(moduleRootDir, "api/handler.go"))
	expectedUserFilePath, _ := filepath.Abs(filepath.Join(moduleRootDir, "models/user.go"))

	t.Run("ScanAndUpdateCache_FindSymbol_CacheHit", func(t *testing.T) {
		testCacheDir := t.TempDir()
		cacheFilePath := filepath.Join(testCacheDir, "symbols.json")

		s, err := New(WithWorkDir("../testdata/multipkg")) // Scanner for the test module
		if err != nil {
			t.Fatalf("New() failed: %v", err)
		}
		s.CachePath = cacheFilePath
		defer func() {
			if err := s.SaveSymbolCache(context.Background()); err != nil {
				t.Errorf("Failed to save symbol cache: %v", err)
			}
		}()

		_, err = s.ScanPackageFromImportPath(context.Background(), apiImportPath)
		if err != nil {
			t.Fatalf("ScanPackageFromImportPath(%s) failed: %v", apiImportPath, err)
		}

		handlerSymbolFullName := apiImportPath + ".Handler"
		loc, err := s.FindSymbolDefinitionLocation(context.Background(), handlerSymbolFullName)
		if err != nil {
			t.Fatalf("FindSymbolDefinitionLocation(%s) after scan failed: %v", handlerSymbolFullName, err)
		}
		if !pathsEqual(loc, expectedHandlerFilePath) {
			t.Errorf("Expected Handler path %s, got %s", expectedHandlerFilePath, loc)
		}

		if err := s.SaveSymbolCache(context.Background()); err != nil {
			t.Fatalf("Explicit save failed: %v", err)
		}

		data, err := os.ReadFile(cacheFilePath)
		if err != nil {
			t.Fatalf("Failed to read cache file: %v", err)
		}

		var loadedCacheContent cacheContent
		if err := json.Unmarshal(data, &loadedCacheContent); err != nil {
			t.Fatalf("Failed to unmarshal cache content: %v", err)
		}

		// In the symbol cache, file paths are stored relative to the module root (s.locator.RootDir())
		// which is testdata/multipkg in this test case.
		// So, expected path in cache is "api/handler.go", not the full absolute path.
		expectedHandlerRelPath := "api/handler.go"

		if actualPathInCache, ok := loadedCacheContent.Symbols[handlerSymbolFullName]; !ok {
			t.Errorf("Cache Symbols map does not contain %s. Content: %+v", handlerSymbolFullName, loadedCacheContent.Symbols)
		} else if actualPathInCache != expectedHandlerRelPath {
			t.Errorf("Cache Symbols map for %s has path %s, expected %s", handlerSymbolFullName, actualPathInCache, expectedHandlerRelPath)
		}

		if fileMeta, ok := loadedCacheContent.Files[expectedHandlerRelPath]; !ok {
			t.Errorf("Cache Files map does not contain entry for handler file %s. Content: %+v", expectedHandlerRelPath, loadedCacheContent.Files)
		} else {
			foundHandlerInFileMeta := false
			for _, symName := range fileMeta.Symbols {
				if symName == "Handler" {
					foundHandlerInFileMeta = true
					break
				}
			}
			if !foundHandlerInFileMeta {
				t.Errorf("Handler symbol not found in FileMetadata for %s. Symbols: %v", expectedHandlerRelPath, fileMeta.Symbols)
			}
		}

		_, err = s.ScanPackageFromImportPath(context.Background(), modelsImportPath)
		if err != nil {
			t.Fatalf("ScanPackageFromImportPath(%s) failed: %v", modelsImportPath, err)
		}

		userSymbolFullName := modelsImportPath + ".User"
		locUser, errUser := s.FindSymbolDefinitionLocation(context.Background(), userSymbolFullName)
		if errUser != nil {
			t.Fatalf("FindSymbolDefinitionLocation(%s) after scan failed: %v", userSymbolFullName, errUser)
		}
		if !pathsEqual(locUser, expectedUserFilePath) {
			t.Errorf("Expected User path %s, got %s", expectedUserFilePath, locUser)
		}
	})

	t.Run("FindSymbol_CacheMiss_FallbackScanSuccess", func(t *testing.T) {
		testCacheDir := t.TempDir()
		cacheFilePath := filepath.Join(testCacheDir, "symbols_fallback.json")

		s, err := New(WithWorkDir("../testdata/multipkg"))
		if err != nil {
			t.Fatalf("New() failed: %v", err)
		}
		s.CachePath = cacheFilePath
		defer func() { s.SaveSymbolCache(context.Background()) }()

		userSymbolFullName := modelsImportPath + ".User"
		loc, err := s.FindSymbolDefinitionLocation(context.Background(), userSymbolFullName)
		if err != nil {
			t.Fatalf("FindSymbolDefinitionLocation(%s) with empty cache failed: %v", userSymbolFullName, err)
		}
		if !pathsEqual(loc, expectedUserFilePath) {
			t.Errorf("Expected User path %s, got %s after fallback scan", expectedUserFilePath, loc)
		}

		locHit, errHit := s.FindSymbolDefinitionLocation(context.Background(), userSymbolFullName)
		if errHit != nil {
			t.Fatalf("FindSymbolDefinitionLocation(%s) second time (expect cache hit) failed: %v", userSymbolFullName, errHit)
		}
		if !pathsEqual(locHit, expectedUserFilePath) {
			t.Errorf("Expected User path %s on cache hit, got %s", expectedUserFilePath, locHit)
		}
	})

	t.Run("FindSymbol_CacheStale_FallbackScanSuccess", func(t *testing.T) {
		testCacheDir := t.TempDir()
		cacheFilePath := filepath.Join(testCacheDir, "symbols_stale.json")

		s, err := New(WithWorkDir("../testdata/multipkg"))
		if err != nil {
			t.Fatalf("New() failed: %v", err)
		}
		s.CachePath = cacheFilePath
		defer func() { s.SaveSymbolCache(context.Background()) }()

		staleUserSymbol := modelsImportPath + ".User"
		staleFileRelativePath := "models/non_existent_user.go"

		prefilledCacheContent := cacheContent{
			Symbols: map[string]string{
				staleUserSymbol: staleFileRelativePath,
			},
			Files: map[string]fileMetadata{
				staleFileRelativePath: {
					Symbols: []string{"User"},
				},
			},
		}
		jsonData, _ := json.MarshalIndent(prefilledCacheContent, "", "  ")
		os.MkdirAll(filepath.Dir(cacheFilePath), 0755)
		os.WriteFile(cacheFilePath, jsonData, 0644)

		loc, err := s.FindSymbolDefinitionLocation(context.Background(), staleUserSymbol)
		if err != nil {
			t.Fatalf("FindSymbolDefinitionLocation for stale entry failed: %v", err)
		}
		if !pathsEqual(loc, expectedUserFilePath) {
			t.Errorf("Expected User path %s after stale cache fallback, got %s", expectedUserFilePath, loc)
		}

		s.SaveSymbolCache(context.Background())

		sVerify, _ := New(WithWorkDir("../testdata/multipkg"))
		sVerify.CachePath = cacheFilePath

		locVerify, errVerify := sVerify.FindSymbolDefinitionLocation(context.Background(), staleUserSymbol)
		if errVerify != nil {
			t.Fatalf("FindSymbolDefinitionLocation after stale fix failed: %v", errVerify)
		}
		if !pathsEqual(locVerify, expectedUserFilePath) {
			t.Errorf("Cache not updated correctly. Expected %s, got %s", expectedUserFilePath, locVerify)
		}
	})

	t.Run("FindSymbol_NonExistentSymbol_FallbackScanFail", func(t *testing.T) {
		testCacheDir := t.TempDir()
		cacheFilePath := filepath.Join(testCacheDir, "symbols_nonexist.json")

		s, err := New(WithWorkDir("../testdata/multipkg"))
		if err != nil {
			t.Fatalf("New() failed: %v", err)
		}
		s.CachePath = cacheFilePath
		defer func() { s.SaveSymbolCache(context.Background()) }()

		nonExistentSymbol := modelsImportPath + ".NonExistentType"
		_, err = s.FindSymbolDefinitionLocation(context.Background(), nonExistentSymbol)
		if err == nil {
			t.Fatalf("FindSymbolDefinitionLocation for non-existent symbol %s should have failed", nonExistentSymbol)
		}
		expectedErrorSubString := "not found in package"
		if !strings.Contains(err.Error(), expectedErrorSubString) {
			t.Errorf("Expected error for non-existent symbol to contain %q, got: %v", expectedErrorSubString, err)
		}
	})

	t.Run("CacheDisabled_NoCacheFileCreated", func(t *testing.T) {
		testCacheDir := t.TempDir()
		cacheFilePath := filepath.Join(testCacheDir, "symbols_disabled.json")

		s, err := New(WithWorkDir("../testdata/multipkg"))
		if err != nil {
			t.Fatalf("New() failed: %v", err)
		}
		s.CachePath = ""

		defer func() { s.SaveSymbolCache(context.Background()) }()

		_, err = s.ScanPackageFromImportPath(context.Background(), apiImportPath)
		if err != nil {
			t.Fatalf("ScanPackageFromImportPath failed: %v", err)
		}

		if errSave := s.SaveSymbolCache(context.Background()); errSave != nil {
			t.Errorf("SaveSymbolCache() with disabled cache errored: %v", errSave)
		}

		if _, err := os.Stat(cacheFilePath); !os.IsNotExist(err) {
			t.Errorf("Cache file %s was created even when s.CachePath is empty", cacheFilePath)
		}
	})
}

func TestListExportedSymbols(t *testing.T) {
	ctx := context.Background()
	s, err := New(WithGoModuleResolver())
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	symbols, err := s.ListExportedSymbols(ctx, "strings")
	if err != nil {
		t.Fatalf("s.ListExportedSymbols failed: %v", err)
	}

	// This is the list of all exported functions and types from the 'strings' package.
	// Methods of types (like Builder.Len) are excluded.
	expectedSymbols := []string{
		"Builder",
		"Clone",
		"Compare",
		"Contains",
		"ContainsAny",
		"ContainsFunc",
		"ContainsRune",
		"Count",
		"Cut",
		"CutPrefix",
		"CutSuffix",
		"EqualFold",
		"Fields",
		"FieldsFunc",
		"FieldsFuncSeq",
		"FieldsSeq",
		"HasPrefix",
		"HasSuffix",
		"Index",
		"IndexAny",
		"IndexByte",
		"IndexFunc",
		"IndexRune",
		"Join",
		"LastIndex",
		"LastIndexAny",
		"LastIndexByte",
		"LastIndexFunc",
		"Lines",
		"Map",
		"NewReader",
		"NewReplacer",
		"Reader",
		"Repeat",
		"Replace",
		"ReplaceAll",
		"Replacer",
		"Split",
		"SplitAfter",
		"SplitAfterN",
		"SplitAfterSeq",
		"SplitN",
		"SplitSeq",
		"Title",
		"ToLower",
		"ToLowerSpecial",
		"ToTitle",
		"ToTitleSpecial",
		"ToUpper",
		"ToUpperSpecial",
		"ToValidUTF8",
		"Trim",
		"TrimFunc",
		"TrimLeft",
		"TrimLeftFunc",
		"TrimPrefix",
		"TrimRight",
		"TrimRightFunc",
		"TrimSpace",
		"TrimSuffix",
	}

	// strings.CutLast was added in Go 1.27.
	if version.Compare(runtime.Version(), "go1.27") >= 0 {
		expectedSymbols = append(expectedSymbols, "CutLast")
	}

	if diff := cmp.Diff(expectedSymbols, symbols); diff != "" {
		// The test output for `symbols` is already sorted by the function itself.
		// We just need to ensure our expected list is also sorted for a stable diff.
		sort.Strings(expectedSymbols)
		if diff = cmp.Diff(expectedSymbols, symbols); diff != "" {
			t.Errorf("ListExportedSymbols() mismatch (-want +got):\n%s", diff)
		}
	}
}

func pathsEqual(p1, p2 string) bool {
	abs1, err1 := filepath.Abs(p1)
	if err1 != nil {
		return false
	}
	abs2, err2 := filepath.Abs(p2)
	if err2 != nil {
		return false
	}
	if strings.EqualFold(abs1, abs2) {
		return true
	}
	return abs1 == abs2
}

func TestScannerWithExternalTypeOverrides(t *testing.T) {
	s, err := New(WithWorkDir("../testdata/externaltypes"))
	if err != nil {
		t.Fatalf("Failed to create Scanner for testdata/externaltypes: %v", err)
	}

	overrides := scanner.ExternalTypeOverride{
		"github.com/google/uuid.UUID": {
			Name:    "UUID",
			PkgPath: "github.com/google/uuid",
			Kind:    scanner.AliasKind,
			Underlying: &scanner.FieldType{
				Name:      "string",
				IsBuiltin: true,
			},
		},
		"example.com/somepkg.Time": {
			Name:    "MyTime",
			PkgPath: "mypkg",
			Kind:    scanner.StructKind, // Treat as a struct
		},
	}
	s.SetExternalTypeOverrides(context.Background(), overrides)

	pkgInfo, err := s.ScanPackageFromImportPath(context.Background(), "example.com/externaltypes")
	if err != nil {
		t.Fatalf("Failed to scan package 'example.com/externaltypes': %v", err)
	}

	foundObjectWithUUID := false
	foundObjectWithCustomTime := false

	for _, typeInfo := range pkgInfo.Types {
		if typeInfo.Name == "ObjectWithUUID" {
			foundObjectWithUUID = true
			if typeInfo.Struct == nil {
				t.Errorf("ObjectWithUUID should be a struct, but it's not")
				continue
			}
			for _, field := range typeInfo.Struct.Fields {
				if field.Name == "ID" {
					if field.Type.Name != "UUID" || field.Type.PkgName != "github.com/google/uuid" {
						t.Errorf("Expected field ID of ObjectWithUUID to be overridden to 'github.com/google/uuid.UUID', got '%s.%s'", field.Type.PkgName, field.Type.Name)
					}
					if !field.Type.IsResolvedByConfig {
						t.Errorf("Expected field ID of ObjectWithUUID to have IsResolvedByConfig=true")
					}
					resolvedType, errResolve := s.ResolveType(context.Background(), field.Type)
					if errResolve != nil {
						t.Errorf("s.ResolveType() for overridden type should not error, got %v", errResolve)
					}
					if resolvedType == nil {
						t.Errorf("s.ResolveType() for overridden type should return the synthetic TypeInfo, got nil")
					} else if resolvedType.Name != "UUID" {
						t.Errorf("Resolved type has wrong name: got %s, want UUID", resolvedType.Name)
					}
				}
			}
		} else if typeInfo.Name == "ObjectWithCustomTime" {
			foundObjectWithCustomTime = true
			if typeInfo.Struct == nil {
				t.Errorf("ObjectWithCustomTime should be a struct, but it's not")
				continue
			}
			for _, field := range typeInfo.Struct.Fields {
				if field.Name == "Timestamp" {
					if field.Type.Name != "MyTime" || field.Type.PkgName != "mypkg" {
						t.Errorf("Expected field Timestamp of ObjectWithCustomTime to be overridden to 'mypkg.MyTime', got '%s.%s'", field.Type.PkgName, field.Type.Name)
					}
					if !field.Type.IsResolvedByConfig {
						t.Errorf("Expected field Timestamp of ObjectWithCustomTime to have IsResolvedByConfig=true")
					}
				}
			}
		}
	}

	if !foundObjectWithUUID {
		t.Errorf("Type 'ObjectWithUUID' not found in scanned package")
	}
	if !foundObjectWithCustomTime {
		t.Errorf("Type 'ObjectWithCustomTime' not found in scanned package")
	}

	sBasic, err := New(WithWorkDir("../testdata/basic"))
	if err != nil {
		t.Fatalf("Failed to create scanner for basic testdata: %v", err)
	}
	sBasic.SetExternalTypeOverrides(context.Background(), nil)

	pkgBasic, err := sBasic.ScanPackageFromImportPath(context.Background(), "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/basic")
	if err != nil {
		t.Fatalf("Failed to scan basic package: %v", err)
	}
	foundUserStruct := false
	for _, typeInfo := range pkgBasic.Types {
		if typeInfo.Name == "User" {
			foundUserStruct = true
			if typeInfo.Struct == nil {
				t.Errorf("User type should be a struct but it's not.")
				continue
			}
			if len(typeInfo.Struct.Fields) > 0 {
				idField := typeInfo.Struct.Fields[0]
				if idField.Name == "ID" {
					if idField.Type.IsResolvedByConfig {
						t.Errorf("Field ID in User should not have IsResolvedByConfig=true when no overrides are active for it")
					}
					if idField.Type.Name != "int" { // Assuming basic.User.ID is int
						t.Errorf("User.ID expected type 'int', got '%s'", idField.Type.Name)
					}
				}
			}
			break
		}
	}
	if !foundUserStruct {
		t.Errorf("User struct not found in basic package scan without overrides")
	}
}

// Helper function to check if a slice of strings contains a specific string.
func containsString(slice []string, str string) bool {
	for _, item := range slice {
		if item == str {
			return true
		}
	}
	return false
}

// Helper function to check if two slices of strings are equal regardless of order.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aCopy := make([]string, len(a))
	bCopy := make([]string, len(b))
	copy(aCopy, a)
	copy(bCopy, b)
	sort.Strings(aCopy)
	sort.Strings(bCopy)
	return reflect.DeepEqual(aCopy, bCopy)
}

func TestScanFilesAndGetUnscanned(t *testing.T) {
	// Setup: Initialize scanner relative to the 'testdata/scanfiles' module
	s, err := New(WithWorkDir("../testdata/scanfiles"))
	if err != nil {
		t.Fatalf("New() for scanfiles module failed: %v", err)
	}
	if s.locator.ModulePath() != "example.com/scanfiles" {
		t.Fatalf("Expected module path 'example.com/scanfiles', got '%s'", s.locator.ModulePath())
	}

	coreUserPathAbs, _ := filepath.Abs("../testdata/scanfiles/core/user.go")
	coreItemPathAbs, _ := filepath.Abs("../testdata/scanfiles/core/item.go")
	coreEmptyPathAbs, _ := filepath.Abs("../testdata/scanfiles/core/empty.go")
	// handlersUserHandlerPathAbs, _ := filepath.Abs("../testdata/scanfiles/handlers/user_handler.go")

	corePkgImportPath := "example.com/scanfiles/core"
	// handlersPkgImportPath := "example.com/scanfiles/handlers"

	t.Run("ScanFiles_RelativePath_CoreUser", func(t *testing.T) {
		sTest, _ := New(WithWorkDir("../testdata/scanfiles")) // Fresh scanner for this subtest

		// Change CWD for this specific sub-test to test CWD-relative paths robustly
		originalCwd, _ := os.Getwd()
		os.Chdir("../testdata/scanfiles/core") // Change to core directory
		defer os.Chdir(originalCwd)            // Change back

		pkgInfo, err := sTest.ScanFiles(context.Background(), []string{"user.go"}) // Relative to new CWD: testdata/scanfiles/core
		if err != nil {
			t.Fatalf("ScanFiles for core/user.go (relative) failed: %v", err)
		}
		if pkgInfo == nil {
			t.Fatal("ScanFiles returned nil pkgInfo")
		}
		if !containsString(pkgInfo.Files, coreUserPathAbs) || len(pkgInfo.Files) != 1 {
			t.Errorf("ScanFiles: expected Files to contain only %s, got %v", coreUserPathAbs, pkgInfo.Files)
		}
		if _, visited := sTest.visitedFiles[coreUserPathAbs]; !visited {
			t.Errorf("ScanFiles: %s should be marked as visited", coreUserPathAbs)
		}
		if typeUser := findType(pkgInfo.Types, "User"); typeUser == nil {
			t.Error("Type 'User' not found in pkgInfo from ScanFiles(user.go)")
		}
	})

	t.Run("ScanFiles_AbsolutePath_CoreItem", func(t *testing.T) {
		sTest, _ := New(WithWorkDir("../testdata/scanfiles"))
		pkgInfo, err := sTest.ScanFiles(context.Background(), []string{coreItemPathAbs})
		if err != nil {
			t.Fatalf("ScanFiles for %s (absolute) failed: %v", coreItemPathAbs, err)
		}
		if !containsString(pkgInfo.Files, coreItemPathAbs) || len(pkgInfo.Files) != 1 {
			t.Errorf("ScanFiles: expected Files to contain only %s, got %v", coreItemPathAbs, pkgInfo.Files)
		}
		if _, visited := sTest.visitedFiles[coreItemPathAbs]; !visited {
			t.Errorf("ScanFiles: %s should be marked as visited", coreItemPathAbs)
		}
		if typeItem := findType(pkgInfo.Types, "Item"); typeItem == nil {
			t.Error("Type 'Item' not found in pkgInfo from ScanFiles(item.go)")
		}
	})

	t.Run("ScanFiles_ModuleQualifiedPath_CoreUser", func(t *testing.T) {
		sTest, _ := New(WithWorkDir("../testdata/scanfiles")) // Scanner initialized at scanfiles module root
		moduleQualifiedPath := "example.com/scanfiles/core/user.go"
		pkgInfo, err := sTest.ScanFiles(context.Background(), []string{moduleQualifiedPath})
		if err != nil {
			t.Fatalf("ScanFiles for %s (module-qualified) failed: %v", moduleQualifiedPath, err)
		}
		if !containsString(pkgInfo.Files, coreUserPathAbs) || len(pkgInfo.Files) != 1 {
			t.Errorf("ScanFiles: expected Files to contain only %s for module-qualified path, got %v", coreUserPathAbs, pkgInfo.Files)
		}
		if _, visited := sTest.visitedFiles[coreUserPathAbs]; !visited {
			t.Errorf("ScanFiles: %s should be marked as visited via module-qualified path", coreUserPathAbs)
		}
	})

	t.Run("ScanFiles_MultipleCalls_VisitedSkipped", func(t *testing.T) {
		sTest, _ := New(WithWorkDir("../testdata/scanfiles"))
		// First call: scan user.go
		_, err := sTest.ScanFiles(context.Background(), []string{coreUserPathAbs})
		if err != nil {
			t.Fatalf("First ScanFiles call failed: %v", err)
		}

		// Second call: scan user.go (already visited) and item.go (new)
		pkgInfo, err := sTest.ScanFiles(context.Background(), []string{coreUserPathAbs, coreItemPathAbs})
		if err != nil {
			t.Fatalf("Second ScanFiles call failed: %v", err)
		}

		if !containsString(pkgInfo.Files, coreItemPathAbs) || len(pkgInfo.Files) != 1 {
			t.Errorf("Second ScanFiles: expected Files to contain only newly scanned %s, got %v", coreItemPathAbs, pkgInfo.Files)
		}
		if typeItem := findType(pkgInfo.Types, "Item"); typeItem == nil {
			t.Error("Type 'Item' not found in second ScanFiles call (should be from item.go)")
		}
		if typeUser := findType(pkgInfo.Types, "User"); typeUser != nil {
			t.Error("Type 'User' (from user.go) should not be in second ScanFiles result as it was already visited")
		}
		if _, visited := sTest.visitedFiles[coreUserPathAbs]; !visited {
			t.Error("user.go not marked visited")
		}
		if _, visited := sTest.visitedFiles[coreItemPathAbs]; !visited {
			t.Error("item.go not marked visited")
		}
	})

	t.Run("ScanFiles_AllFilesVisited_EmptyResult", func(t *testing.T) {
		sTest, _ := New(WithWorkDir("../testdata/scanfiles"))
		_, err := sTest.ScanFiles(context.Background(), []string{coreUserPathAbs}) // Visit user.go
		if err != nil {
			t.Fatalf("Failed to scan user.go: %v", err)
		}

		pkgInfo, err := sTest.ScanFiles(context.Background(), []string{coreUserPathAbs}) // Scan again
		if err != nil {
			t.Fatalf("Second scan of user.go failed: %v", err)
		}
		if len(pkgInfo.Files) != 0 {
			t.Errorf("Expected Files to be empty when scanning already visited file, got %v", pkgInfo.Files)
		}
		if len(pkgInfo.Types) != 0 || len(pkgInfo.Functions) != 0 || len(pkgInfo.Constants) != 0 {
			t.Error("Expected no symbols when scanning already visited file")
		}
	})

	t.Run("UnscannedGoFiles_CorePackage", func(t *testing.T) {
		sTest, _ := New(WithWorkDir("../testdata/scanfiles"))

		// Initially, all files in core should be unscanned
		unscanned, err := sTest.UnscannedGoFiles(corePkgImportPath)
		if err != nil {
			t.Fatalf("UnscannedGoFiles initial failed: %v", err)
		}
		expectedUnscannedInitial := []string{coreEmptyPathAbs, coreItemPathAbs, coreUserPathAbs}
		if !equalStringSlices(unscanned, expectedUnscannedInitial) {
			t.Errorf("Initial unscanned files: expected %v, got %v", expectedUnscannedInitial, unscanned)
		}

		// Scan user.go
		_, err = sTest.ScanFiles(context.Background(), []string{coreUserPathAbs})
		if err != nil {
			t.Fatalf("ScanFiles(user.go) failed: %v", err)
		}

		unscannedAfterUser, err := sTest.UnscannedGoFiles(corePkgImportPath)
		if err != nil {
			t.Fatalf("UnscannedGoFiles after user.go scan failed: %v", err)
		}
		expectedUnscannedAfterUser := []string{coreEmptyPathAbs, coreItemPathAbs}
		if !equalStringSlices(unscannedAfterUser, expectedUnscannedAfterUser) {
			t.Errorf("Unscanned after user.go: expected %v, got %v", expectedUnscannedAfterUser, unscannedAfterUser)
		}

		// Scan item.go and empty.go
		_, err = sTest.ScanFiles(context.Background(), []string{coreItemPathAbs, coreEmptyPathAbs})
		if err != nil {
			t.Fatalf("ScanFiles(item.go, empty.go) failed: %v", err)
		}

		unscannedAllScanned, err := sTest.UnscannedGoFiles(corePkgImportPath)
		if err != nil {
			t.Fatalf("UnscannedGoFiles after all core files scanned failed: %v", err)
		}
		if len(unscannedAllScanned) != 0 {
			t.Errorf("Expected no unscanned files after all core files scanned, got %v", unscannedAllScanned)
		}
	})

	t.Run("ScanPackageFromFilePath_RespectsVisitedFiles", func(t *testing.T) {
		sTest, _ := New(WithWorkDir("../testdata/scanfiles"))
		// Scan core/user.go via ScanFiles first
		_, err := sTest.ScanFiles(context.Background(), []string{coreUserPathAbs})
		if err != nil {
			t.Fatalf("ScanFiles(user.go) failed: %v", err)
		}

		// Now ScanPackageFromFilePath for the whole core package
		// It should only parse item.go and empty.go as user.go is visited
		pkgInfo, err := sTest.ScanPackageFromFilePath(context.Background(), "../testdata/scanfiles/core")
		if err != nil {
			t.Fatalf("ScanPackageFromFilePath(core) failed: %v", err)
		}

		if pkgInfo == nil {
			t.Fatal("ScanPackageFromFilePath returned nil pkgInfo")
		}

		// pkgInfo.Files from ScanPackageFromFilePath should only contain newly parsed files (item.go, empty.go)
		expectedFiles := []string{coreItemPathAbs, coreEmptyPathAbs}
		if !equalStringSlices(pkgInfo.Files, expectedFiles) {
			t.Errorf("ScanPackageFromFilePath(core) after ScanFiles(user.go): expected Files %v, got %v", expectedFiles, pkgInfo.Files)
		}
		if findType(pkgInfo.Types, "Item") == nil {
			t.Error("Type 'Item' should be in ScanPackageFromFilePath result")
		}
		if findType(pkgInfo.Types, "User") != nil { // User was already visited
			t.Error("Type 'User' should NOT be in this ScanPackageFromFilePath result (already visited)")
		}
		if _, visited := sTest.visitedFiles[coreItemPathAbs]; !visited {
			t.Error("item.go not marked visited after ScanPackageFromFilePath")
		}
		if _, visited := sTest.visitedFiles[coreEmptyPathAbs]; !visited {
			t.Error("empty.go not marked visited after ScanPackageFromFilePath")
		}

		// Check package cache for the import path
		sTest.mu.RLock()
		cachedInfo, found := sTest.packageCache[corePkgImportPath]
		sTest.mu.RUnlock()
		if !found {
			t.Errorf("PackageInfo for %s not found in packageCache after ScanPackageFromFilePath", corePkgImportPath)
		} else {
			// The cached info should be the result of this ScanPackageFromFilePath call
			if !equalStringSlices(cachedInfo.Files, expectedFiles) {
				t.Errorf("Cached PackageInfo.Files: expected %v, got %v", expectedFiles, cachedInfo.Files)
			}
		}
	})
}

func findType(types []*scanner.TypeInfo, name string) *scanner.TypeInfo {
	for _, ti := range types {
		if ti.Name == name {
			return ti
		}
	}
	return nil
}

func TestImplements(t *testing.T) {
	ctx := context.Background()
	// Setup: Initialize scanner relative to the project root,
	// as testdata/implements is not a separate Go module.
	// We'll use ScanPackageFromFilePath with a direct path.
	s, err := New(WithWorkDir(".")) // Assuming tests are run from project root
	if err != nil {
		t.Fatalf("New(\".\") failed: %v", err)
	}

	// Scan the testdata/implements package
	// Note: ScanPackageFromFilePath's import path resolution might be tricky for non-module testdata.
	// We rely on the absolute path for scanning and then manually construct import path if needed by TypeInfo.
	// However, Implements itself doesn't directly use import paths from TypeInfo for resolution,
	// it uses PkgName and Name from FieldType, which are derived by the scanner.
	// The crucial part is that all types and functions are loaded into one PackageInfo.
	pkgPath := "../testdata/implements"
	pkgInfo, err := s.ScanPackageFromFilePath(ctx, pkgPath)
	if err != nil {
		t.Fatalf("ScanPackageFromFilePath(%q) failed: %v", pkgPath, err)
	}
	if pkgInfo == nil {
		t.Fatalf("ScanPackageFromFilePath(%q) returned nil PackageInfo", pkgPath)
	}
	// For types within the same package, PkgName in FieldType might be empty or the package name.
	// This detail affects how compareFieldTypes works if it were to compare PkgName.
	// Current compareFieldTypes is simple, so this is less of an issue.

	getType := func(name string) *scanner.TypeInfo {
		for _, ti := range pkgInfo.Types {
			if ti.Name == name {
				return ti
			}
		}
		t.Fatalf("Type %q not found in package %q", name, pkgInfo.Name)
		return nil
	}

	tests := []struct {
		name                string
		structName          string
		interfaceName       string
		expectedToImplement bool
	}{
		// Basic cases
		{"SimpleStruct_SimpleInterface", "SimpleStruct", "SimpleInterface", true},
		{"SimpleStruct_EmptyInterface", "SimpleStruct", "EmptyInterface", true},
		{"PointerReceiverStruct_PointerReceiverInterface", "PointerReceiverStruct", "PointerReceiverInterface", true},
		{"ValueReceiverStruct_ValueReceiverInterface", "ValueReceiverStruct", "ValueReceiverInterface", true},
		{"ComplexTypeStruct_ComplexTypeInterface", "ComplexTypeStruct", "ComplexTypeInterface", true},

		// Negative cases: Method mismatches
		{"MissingMethodStruct_SimpleInterface", "MissingMethodStruct", "SimpleInterface", false},
		{"WrongNameStruct_SimpleInterface", "WrongNameStruct", "SimpleInterface", false},
		{"WrongParamCountStruct_SimpleInterface", "WrongParamCountStruct", "SimpleInterface", false},
		{"WrongParamTypeStruct_SimpleInterface", "WrongParamTypeStruct", "SimpleInterface", false},
		{"WrongReturnCountStruct_SimpleInterface", "WrongReturnCountStruct", "SimpleInterface", false},
		{"WrongReturnTypeStruct_SimpleInterface", "WrongReturnTypeStruct", "SimpleInterface", false},

		// Negative cases: Interface not implemented
		{"SimpleStruct_UnimplementedInterface", "SimpleStruct", "UnimplementedInterface", false},

		// Edge cases: Empty and no methods
		{"NoMethodStruct_SimpleInterface", "NoMethodStruct", "SimpleInterface", false},
		{"NoMethodStruct_EmptyInterface", "NoMethodStruct", "EmptyInterface", true}, // Any type implements empty interface

		// Receiver type considerations (based on current Implements logic)
		// Implements gathers methods for both T and *T if structCandidate is T.
		{"StructValueReceiverMethodX_InterfaceRequiresMethodX", "StructValueReceiverMethodX", "InterfaceRequiresMethodX", true},
		// StructPointerReceiverMethodX only has (*S) MethodX().
		// Implements(S_TypeInfo, I_TypeInfo) should be true if I just needs MethodX()
		// because *S has MethodX() and Implements considers methods of *S for S_TypeInfo.
		{"StructPointerReceiverMethodX_InterfaceRequiresMethodX", "StructPointerReceiverMethodX", "InterfaceRequiresMethodX", true},

		// Test if a struct with pointer receiver method can satisfy an interface method (no receiver specified on interface method)
		// when the struct itself is passed as a value type candidate.
		// InterfaceValueRecMethod has DoIt(). StructPointerRecMethodForInterfaceValue has (*S) DoIt().
		// Implements(StructPointerRecMethodForInterfaceValue_TypeInfo, InterfaceValueRecMethod_TypeInfo) should be true.
		{"StructPointerRecMethodForInterfaceValue_InterfaceValueRecMethod", "StructPointerRecMethodForInterfaceValue", "InterfaceValueRecMethod", true},

		// Test if a struct with only value receiver can satisfy an interface that might "imply" pointer methods.
		// MyStructForReceiverTest has (t MyStructForReceiverTest) ValRec() and (t *MyStructForReceiverTest) PtrRec()
		// InterfaceForValRec has ValRec()
		// InterfaceForPtrRec has PtrRec()
		{"MyStructForReceiverTest_InterfaceForValRec", "MyStructForReceiverTest", "InterfaceForValRec", true},
		// MyStructForReceiverTest (value type) does not have PtrRec() in its value method set.
		// But Implements will find (*MyStructForReceiverTest) PtrRec() and associate it.
		{"MyStructForReceiverTest_InterfaceForPtrRec", "MyStructForReceiverTest", "InterfaceForPtrRec", true},

		// Nil and invalid inputs (tested directly, not via table for clarity on nil pkgInfo etc.)

		// Type comparison details (assuming compareFieldTypes will be/is correct for slices, maps, pointers)
		{"StructWithAnotherType_InterfaceWithAnotherType", "StructWithAnotherType", "InterfaceWithAnotherType", true},
		{"StructWithDifferentNamedType_InterfaceWithAnotherType", "StructWithDifferentNamedType", "InterfaceWithAnotherType", false},                           // Fails due to YetAnotherType vs AnotherType
		{"StructWithMismatchedPointerForAnotherType_InterfaceWithAnotherType", "StructWithMismatchedPointerForAnotherType", "InterfaceWithAnotherType", false}, // Fails due to pointer mismatch on param/return

		{"StructImplementingSliceMap_InterfaceWithSliceMap", "StructImplementingSliceMap", "InterfaceWithSliceMap", true},
		{"StructMismatchSlice_InterfaceWithSliceMap", "StructMismatchSlice", "InterfaceWithSliceMap", false},       // Assumes compareFieldTypes handles slice element type
		{"StructMismatchMapValue_InterfaceWithSliceMap", "StructMismatchMapValue", "InterfaceWithSliceMap", false}, // Assumes compareFieldTypes handles map value type
		{"StructMismatchMapKey_InterfaceWithSliceMap", "StructMismatchMapKey", "InterfaceWithSliceMap", false},     // Assumes compareFieldTypes handles map key type

		{"StructWithPointerInSlice_InterfaceWithPointerInSlice", "StructWithPointerInSlice", "InterfaceWithPointerInSlice", true},                    // []*int vs []*int
		{"StructWithPointerInSlice_InterfaceWithDifferentPointerInSlice", "StructWithPointerInSlice", "InterfaceWithDifferentPointerInSlice", false}, // []*int vs []*string
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			structCandidate := getType(tt.structName)
			interfaceDef := getType(tt.interfaceName)

			// Pre-checks for test validity
			if structCandidate == nil {
				t.Fatalf("Test setup error: Struct candidate %q not found", tt.structName)
			}
			if structCandidate.Kind != StructKind && tt.structName != "NotAStruct" { // Allow NotAStruct for specific test
				t.Fatalf("Test setup error: Candidate %q is not a struct but Kind=%v", tt.structName, structCandidate.Kind)
			}
			if interfaceDef == nil {
				t.Fatalf("Test setup error: Interface definition %q not found", tt.interfaceName)
			}
			if interfaceDef.Kind != InterfaceKind && tt.interfaceName != "NotAnInterface" { // Allow for specific test
				t.Fatalf("Test setup error: Interface def %q is not an interface but Kind=%v", tt.interfaceName, interfaceDef.Kind)
			}

			// Perform the Implements check
			// Ensure pkgInfo is correctly passed; it's derived from scanning "../testdata/implements"
			// and contains all types and functions from types.go
			actual := s.Implements(ctx, structCandidate, interfaceDef)
			if actual != tt.expectedToImplement {
				t.Errorf("s.Implements(%s, %s): expected %v, got %v", tt.structName, tt.interfaceName, tt.expectedToImplement, actual)
			}
		})
	}

	// Direct tests for nil and invalid inputs
	t.Run("NilInputs", func(t *testing.T) {
		simpleStruct := getType("SimpleStruct")
		simpleInterface := getType("SimpleInterface")

		if s.Implements(ctx, nil, simpleInterface) != false {
			t.Error("s.Implements(nil, interface) should be false")
		}
		if s.Implements(ctx, simpleStruct, nil) != false {
			t.Error("s.Implements(struct, nil) should be false")
		}
	})

	t.Run("InvalidKindInputs", func(t *testing.T) {
		notAStruct := getType("NotAStruct")         // This is an alias to int
		notAnInterface := getType("NotAnInterface") // This is an alias to int
		simpleStruct := getType("SimpleStruct")
		simpleInterface := getType("SimpleInterface")

		if notAStruct.Kind == StructKind { // Defensive check on test data itself
			t.Fatal("Test data error: NotAStruct should not have StructKind")
		}
		if notAnInterface.Kind == InterfaceKind { // Defensive check
			t.Fatal("Test data error: NotAnInterface should not have InterfaceKind")
		}

		if s.Implements(ctx, notAStruct, simpleInterface) != false {
			t.Errorf("s.Implements(NotAStruct, SimpleInterface) expected false, got true. NotAStruct.Kind: %v", notAStruct.Kind)
		}
		if s.Implements(ctx, simpleStruct, notAnInterface) != false {
			t.Errorf("s.Implements(SimpleStruct, NotAnInterface) expected false, got true. NotAnInterface.Kind: %v, Interface field: %+v", notAnInterface.Kind, notAnInterface.Interface)
		}
		// Test case where interfaceDef.Interface is nil (e.g. type alias for interface)
		// The getType function resolves to TypeInfo. If NotAnInterface is an alias type,
		// its TypeInfo.Interface field should be nil.
		// The Implements function checks `interfaceDef.Interface == nil`.
		// `scanner.parseTypeSpec` for alias `type MyI = OtherInterface` would set Underlying.
		// For `type MyI int` where MyI is used as interface, `interfaceDef.Kind` wouldn't be `InterfaceKind`.
		// For `type MyI interface {}` then `interfaceDef.Kind` is `InterfaceKind`.
		// If `type MyI OtherInterface` (not an alias, but embedding), scanner would need to resolve.
		// The `NotAnInterface` type in `types.go` is `type NotAnInterface int`. Its kind is `AliasKind`.
		// So `interfaceDef.Kind != InterfaceKind` check in `Implements` handles this.
	})

	// Test for interfaceDef.Interface being nil (e.g. an alias that isn't an interface kind)
	// This is implicitly covered by InvalidKindInputs if NotAnInterface.Kind is not InterfaceKind.
	// If we had `type AliasToInterface = SimpleInterface`, then its Kind might be InterfaceKind (or AliasKind, depending on scanner).
	// If `AliasKind` and `Underlying` points to an interface, `Implements` would need to handle it.
	// Current `Implements` expects `interfaceDef.Kind == InterfaceKind`.

	// New test cases for slice types
	newSliceTests := []struct {
		name                string
		structName          string
		interfaceName       string
		expectedToImplement bool
	}{
		// Slice of primitive types
		{"MySliceProcessorImpl_SliceProcessor_IntSlice", "MySliceProcessorImpl", "SliceProcessor", true},    // Checks ProcessIntSlice
		{"MySliceProcessorImpl_SliceProcessor_StringSlice", "MySliceProcessorImpl", "SliceProcessor", true}, // Checks ProcessStringSlice

		// Slice of structs
		{"MySliceStructProcessorImpl_SliceStructProcessor_StructSlice", "MySliceStructProcessorImpl", "SliceStructProcessor", true},        // Checks ProcessStructSlice
		{"MySliceStructProcessorImpl_SliceStructProcessor_PointerStructSlice", "MySliceStructProcessorImpl", "SliceStructProcessor", true}, // Checks ProcessPointerStructSlice

		// Slice of pointers to structs
		{"MySliceOfPointerProcessorImpl_SliceOfPointerProcessor_Pointers", "MySliceOfPointerProcessorImpl", "SliceOfPointerProcessor", true},     // Checks ProcessSliceOfPointers
		{"MySliceOfPointerProcessorImpl_SliceOfPointerProcessor_PointerAlias", "MySliceOfPointerProcessorImpl", "SliceOfPointerProcessor", true}, // Checks ProcessSliceOfPointerAlias

		// Negative Test Cases for Slices
		// Mismatched element type in slice parameter
		{"MismatchedSliceProcessorImpl_SliceProcessor_IntParamMismatch", "MismatchedSliceProcessorImpl", "SliceProcessor", false},
		// Mismatched element type in slice return
		{"MismatchedReturnSliceProcessorImpl_SliceProcessor_IntReturnMismatch", "MismatchedReturnSliceProcessorImpl", "SliceProcessor", false},
		// Mismatched struct type in slice parameter
		{"MismatchedSliceStructProcessorImpl_SliceStructProcessor_StructParamMismatch", "MismatchedSliceStructProcessorImpl", "SliceStructProcessor", false},
		// Mismatched pointer-ness in slice parameter (e.g. []MyStruct vs []*MyStruct)
		// Interface `SliceStructProcessor.ProcessPointerStructSlice` expects `[]*MyStruct`.
		// Struct `MismatchedPointerNessSliceStructProcessorImpl.ProcessPointerStructSlice` provides `[]MyStruct`.
		{"MismatchedPointerNess_PointerStructSlice", "MismatchedPointerNessSliceStructProcessorImpl", "SliceStructProcessor", false},
		// Parameter is not a slice when interface expects a slice
		{"NotASliceProcessorImpl_SliceProcessor_ParamNotSlice", "NotASliceProcessorImpl", "SliceProcessor", false},
	}

	for _, tt := range newSliceTests {
		t.Run(tt.name, func(t *testing.T) {
			structCandidate := getType(tt.structName)
			interfaceDef := getType(tt.interfaceName)

			if structCandidate == nil {
				t.Fatalf("Test setup error: Struct candidate %q not found", tt.structName)
			}
			if interfaceDef == nil {
				t.Fatalf("Test setup error: Interface definition %q not found", tt.interfaceName)
			}
			// Assuming structCandidate.Kind and interfaceDef.Kind are correct as per previous tests.

			actual := s.Implements(ctx, structCandidate, interfaceDef)
			if actual != tt.expectedToImplement {
				// Provide more debug info if it fails
				// For example, print the methods of the interface and the methods found on the struct
				var interfaceMethods []string
				if interfaceDef.Interface != nil {
					for _, m := range interfaceDef.Interface.Methods {
						interfaceMethods = append(interfaceMethods, m.Name)
					}
				}

				var structMethodsDetails []string
				// Simplified method collection for debugging output, not as robust as Implements itself
				for _, fn := range pkgInfo.Functions {
					if fn.Receiver != nil && fn.Receiver.Type != nil {
						receiverTypeName := fn.Receiver.Type.Name
						actualReceiverName := receiverTypeName
						if fn.Receiver.Type.IsPointer && strings.HasPrefix(receiverTypeName, "*") {
							actualReceiverName = strings.TrimPrefix(receiverTypeName, "*")
						}
						if actualReceiverName == structCandidate.Name {
							structMethodsDetails = append(structMethodsDetails, fmt.Sprintf("%s (Receiver: %s, IsPointer: %t)", fn.Name, fn.Receiver.Type.Name, fn.Receiver.Type.IsPointer))
						}
					}
				}

				t.Errorf("Implements(%s, %s): expected %v, got %v\nInterface Methods: %v\nStruct Methods Found: %v",
					tt.structName, tt.interfaceName, tt.expectedToImplement, actual,
					interfaceMethods, structMethodsDetails)
			}
		})
	}
}

func TestSaveGoFile_Imports(t *testing.T) {
	ctx := context.Background()
	tempDir := t.TempDir()

	pkgDir := NewPackageDirectory(tempDir, "testpkg")

	goFile := GoFile{
		Imports: map[string]string{
			"fmt":                              "",       // No alias
			"custom/errors":                    "errors", // With alias
			"github.com/example/mypkg":         "mypkg",  // Alias same as package name
			"another.com/library/version":      "",       // No alias
			"github.com/another/util":          "anotherutil",
			"github.com/example/anotherpkg/v2": "apkgv2", // Alias different from last part
		},
		CodeSet: "func Hello() {}",
	}

	filename := "test_output.go"
	err := pkgDir.SaveGoFile(ctx, goFile, filename)
	if err != nil {
		t.Fatalf("SaveGoFile failed: %v", err)
	}

	generatedFilePath := filepath.Join(tempDir, filename)
	content, err := os.ReadFile(generatedFilePath)
	if err != nil {
		t.Fatalf("Failed to read generated file: %v", err)
	}

	expectedImports := []string{
		`import (`,
		`	"another.com/library/version"`, // Sorted by path
		`	errors "custom/errors"`,
		`	"fmt"`,
		`	anotherutil "github.com/another/util"`,
		`	apkgv2 "github.com/example/anotherpkg/v2"`,
		`	mypkg "github.com/example/mypkg"`,
		`)`,
	}
	generatedContent := string(content)

	// Check package declaration
	expectedPackage := "package testpkg"
	if !strings.Contains(generatedContent, expectedPackage) {
		t.Errorf("Generated file does not contain expected package declaration.\nExpected: %s\nGot:\n%s", expectedPackage, generatedContent)
	}

	// Check imports block
	importBlockStartIndex := strings.Index(generatedContent, "import (")
	importBlockEndIndex := strings.Index(generatedContent, ")")
	if importBlockStartIndex == -1 || importBlockEndIndex == -1 || importBlockEndIndex < importBlockStartIndex {
		t.Fatalf("Generated file does not contain a valid import block.\nGot:\n%s", generatedContent)
	}
	actualImportBlock := generatedContent[importBlockStartIndex : importBlockEndIndex+1]

	expectedImportBlock := strings.Join(expectedImports, "\n")

	if actualImportBlock != expectedImportBlock {
		t.Errorf("Generated import block does not match expected.\nExpected:\n%s\nGot:\n%s", expectedImportBlock, actualImportBlock)
	}

	// Check code part
	if !strings.Contains(generatedContent, goFile.CodeSet) {
		t.Errorf("Generated file does not contain expected CodeSet.\nExpected to contain: %s\nGot:\n%s", goFile.CodeSet, generatedContent)
	}

	// Test with empty imports
	goFileNoImports := GoFile{
		Imports: map[string]string{},
		CodeSet: "func NoImports() {}",
	}
	filenameNoImports := "no_imports.go"
	err = pkgDir.SaveGoFile(ctx, goFileNoImports, filenameNoImports)
	if err != nil {
		t.Fatalf("SaveGoFile for no imports failed: %v", err)
	}
	contentNoImports, err := os.ReadFile(filepath.Join(tempDir, filenameNoImports))
	if err != nil {
		t.Fatalf("Failed to read generated file for no imports: %v", err)
	}
	if strings.Contains(string(contentNoImports), "import (") {
		t.Errorf("Generated file for no imports should not contain import block, but got:\n%s", string(contentNoImports))
	}
	if !strings.Contains(string(contentNoImports), goFileNoImports.CodeSet) {
		t.Errorf("Generated file for no imports does not contain CodeSet.\nExpected: %s\nGot:\n%s", goFileNoImports.CodeSet, string(contentNoImports))
	}
}

// findFunction is a helper to find a function by name in PackageInfo.
func findFunction(pkgInfo *scanner.PackageInfo, name string) *scanner.FunctionInfo {
	if pkgInfo == nil {
		return nil
	}
	for _, f := range pkgInfo.Functions {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// findType is a helper to find a type by name in PackageInfo.
// This was already defined in the file, ensure it's used or make a new one if needed.
// func findType(types []*scanner.TypeInfo, name string) *scanner.TypeInfo { ... }

func TestScanner_ScanPackageFromFilePath_Generics(t *testing.T) {
	s, err := New(WithWorkDir("../testdata/generics")) // Scanner relative to the "generics" module/directory
	if err != nil {
		t.Fatalf("New() for generics testdata failed: %v", err)
	}
	// Assuming testdata/generics is not a full Go module for simplicity in this test setup,
	// so ModulePath might be the main project's module path or empty depending on locator behavior.
	// We'll scan the package directly by its path.
	// The import path for types within testdata/generics will be determined by the scanner.
	// Let's assume it forms an import path like "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/generics"
	// if go.mod is not present in testdata/generics.
	// For this test, we are more interested in the structure parsing than cross-package resolution.

	pkgInfo, err := s.ScanPackageFromFilePath(context.Background(), "../testdata/generics")
	if err != nil {
		t.Fatalf("ScanPackageFromFilePath() for './testdata/generics' failed: %v", err)
	}
	if pkgInfo == nil {
		t.Fatal("ScanPackageFromFilePath() for './testdata/generics' returned nil PackageInfo")
	}

	// --- Assertions for Generic Functions ---
	t.Run("GenericFunctions", func(t *testing.T) {
		// 1. func Print[T any](val T)
		fnPrint := findFunction(pkgInfo, "Print")
		if fnPrint == nil {
			t.Fatal("Function 'Print' not found")
		}
		if len(fnPrint.TypeParams) != 1 {
			t.Errorf("Expected 1 type parameter for 'Print', got %d", len(fnPrint.TypeParams))
		} else {
			tp := fnPrint.TypeParams[0]
			if tp.Name != "T" {
				t.Errorf("Expected type parameter name 'T', got '%s'", tp.Name)
			}
			if tp.Constraint == nil || tp.Constraint.Name != "any" || !tp.Constraint.IsBuiltin || !tp.Constraint.IsConstraint {
				t.Errorf("Expected constraint 'any' for 'T', got %v", tp.Constraint)
			}
		}
		if len(fnPrint.Parameters) != 1 || fnPrint.Parameters[0].Type == nil || fnPrint.Parameters[0].Type.Name != "T" || !fnPrint.Parameters[0].Type.IsTypeParam {
			t.Errorf("Expected parameter 'val T' (as type param) for 'Print', got %v", fnPrint.Parameters)
		}

		// 2. func PrintStringer[T Stringer](val T)
		fnPrintStringer := findFunction(pkgInfo, "PrintStringer")
		if fnPrintStringer == nil {
			t.Fatal("Function 'PrintStringer' not found")
		}
		if len(fnPrintStringer.TypeParams) != 1 {
			t.Errorf("Expected 1 type parameter for 'PrintStringer', got %d", len(fnPrintStringer.TypeParams))
		} else {
			tp := fnPrintStringer.TypeParams[0]
			if tp.Name != "T" {
				t.Errorf("Expected type parameter name 'T', got '%s'", tp.Name)
			}
			if tp.Constraint == nil || tp.Constraint.Name != "Stringer" || tp.Constraint.IsBuiltin { // Stringer is not a builtin
				t.Errorf("Expected constraint 'Stringer' for 'T', got %v", tp.Constraint)
			}
		}

		// 3. func AreEqual[T comparable](a, b T) bool
		fnAreEqual := findFunction(pkgInfo, "AreEqual")
		if fnAreEqual == nil {
			t.Fatal("Function 'AreEqual' not found")
		}
		if len(fnAreEqual.TypeParams) != 1 {
			t.Errorf("Expected 1 type parameter for 'AreEqual', got %d", len(fnAreEqual.TypeParams))
		} else {
			tp := fnAreEqual.TypeParams[0]
			if tp.Name != "T" {
				t.Errorf("Expected type parameter name 'T', got '%s'", tp.Name)
			}
			if tp.Constraint == nil || tp.Constraint.Name != "comparable" || !tp.Constraint.IsBuiltin || !tp.Constraint.IsConstraint {
				t.Errorf("Expected constraint 'comparable' for 'T', got %v", tp.Constraint)
			}
		}
		if len(fnAreEqual.Results) != 1 || fnAreEqual.Results[0].Type.Name != "bool" {
			t.Errorf("Expected return type 'bool' for 'AreEqual', got %v", fnAreEqual.Results)
		}
	})

	// --- Assertions for Generic Structs ---
	t.Run("GenericStructs", func(t *testing.T) {
		// 1. type List[T any] struct { items []T; Value T }
		stList := findType(pkgInfo.Types, "List")
		if stList == nil {
			t.Fatal("Struct 'List' not found")
		}
		if stList.Kind != scanner.StructKind {
			t.Errorf("Expected 'List' to be StructKind, got %v", stList.Kind)
		}
		if len(stList.TypeParams) != 1 {
			t.Fatalf("Expected 1 type parameter for 'List', got %d", len(stList.TypeParams))
		}
		if stList.TypeParams[0].Name != "T" || stList.TypeParams[0].Constraint.Name != "any" {
			t.Errorf("Incorrect type parameter for 'List': %+v", stList.TypeParams[0])
		}
		fieldValue := findField(stList.Struct, "Value")
		if fieldValue == nil || fieldValue.Type.Name != "T" || !fieldValue.Type.IsTypeParam {
			t.Errorf("'List.Value' field: expected type 'T' (as type param), got %+v", fieldValue)
		}
		fieldItems := findField(stList.Struct, "items")
		if fieldItems == nil || !fieldItems.Type.IsSlice || fieldItems.Type.Elem == nil || fieldItems.Type.Elem.Name != "T" || !fieldItems.Type.Elem.IsTypeParam {
			t.Errorf("'List.items' field: expected type '[]T' (T as type param), got %+v", fieldItems)
		}

		// 2. type KeyValue[K comparable, V any] struct { Key K; Value V }
		stKeyValue := findType(pkgInfo.Types, "KeyValue")
		if stKeyValue == nil {
			t.Fatal("Struct 'KeyValue' not found")
		}
		if len(stKeyValue.TypeParams) != 2 {
			t.Fatalf("Expected 2 type parameters for 'KeyValue', got %d", len(stKeyValue.TypeParams))
		}
		tpK_kv := stKeyValue.TypeParams[0]
		tpV_kv := stKeyValue.TypeParams[1]
		if tpK_kv.Name != "K" || tpK_kv.Constraint.Name != "comparable" {
			t.Errorf("Incorrect type parameter K for 'KeyValue': %+v", tpK_kv)
		}
		if tpV_kv.Name != "V" || tpV_kv.Constraint.Name != "any" {
			t.Errorf("Incorrect type parameter V for 'KeyValue': %+v", tpV_kv)
		}
		fieldKey_kv := findField(stKeyValue.Struct, "Key")
		if fieldKey_kv == nil || fieldKey_kv.Type.Name != "K" || !fieldKey_kv.Type.IsTypeParam {
			t.Errorf("'KeyValue.Key' field: expected type 'K' (as type param), got %+v", fieldKey_kv)
		}
		fieldValue_kv := findField(stKeyValue.Struct, "Value")
		if fieldValue_kv == nil || fieldValue_kv.Type.Name != "V" || !fieldValue_kv.Type.IsTypeParam {
			t.Errorf("'KeyValue.Value' field: expected type 'V' (as type param), got %+v", fieldValue_kv)
		}
	})

	// --- Assertions for Instantiated Generic Types & Aliases ---
	t.Run("InstantiatedAndAliases", func(t *testing.T) {
		// 1. Type alias StringList = List[string]
		aliasStringList := findType(pkgInfo.Types, "StringList")
		if aliasStringList == nil {
			t.Fatal("Type alias 'StringList' not found")
		}
		if aliasStringList.Kind != scanner.AliasKind {
			t.Errorf("Expected 'StringList' to be AliasKind, got %v", aliasStringList.Kind)
		}
		if aliasStringList.Underlying == nil {
			t.Fatalf("'StringList' underlying type is nil")
		}
		if aliasStringList.Underlying.Name != "List" { // Base generic type name
			t.Errorf("Expected underlying base type 'List' for 'StringList', got '%s'", aliasStringList.Underlying.Name)
		}
		if len(aliasStringList.Underlying.TypeArgs) != 1 {
			t.Fatalf("Expected 1 type argument for 'StringList' (List[string]), got %d", len(aliasStringList.Underlying.TypeArgs))
		} else {
			typeArg := aliasStringList.Underlying.TypeArgs[0]
			if typeArg.Name != "string" || !typeArg.IsBuiltin {
				t.Errorf("Expected type argument 'string' for 'StringList', got %+v", typeArg)
			}
		}
		if strRep := aliasStringList.Underlying.String(); strRep != "List[string]" {
			t.Errorf("Expected String() for StringList underlying to be 'List[string]', got '%s'", strRep)
		}

		// 2. Non-generic struct Container with generic fields
		stContainer := findType(pkgInfo.Types, "Container")
		if stContainer == nil {
			t.Fatal("Struct 'Container' not found")
		}
		fieldIntList := findField(stContainer.Struct, "IntList")
		if fieldIntList == nil {
			t.Fatal("'Container.IntList' not found")
		}
		if fieldIntList.Type.Name != "List" || len(fieldIntList.Type.TypeArgs) != 1 || fieldIntList.Type.TypeArgs[0].Name != "int" {
			t.Errorf("'Container.IntList' expected List[int], got %s with args %+v", fieldIntList.Type.Name, fieldIntList.Type.TypeArgs)
		}
		if strRep := fieldIntList.Type.String(); strRep != "List[int]" {
			t.Errorf("Expected String() for IntList to be 'List[int]', got '%s'", strRep)
		}

		fieldKV := findField(stContainer.Struct, "KV")
		if fieldKV == nil {
			t.Fatal("'Container.KV' not found")
		}
		if fieldKV.Type.Name != "KeyValue" || len(fieldKV.Type.TypeArgs) != 2 {
			t.Fatalf("'Container.KV' expected KeyValue[?,?], got %s with %d args", fieldKV.Type.Name, len(fieldKV.Type.TypeArgs))
		}
		typeArgK_kv := fieldKV.Type.TypeArgs[0]
		typeArgV_kv := fieldKV.Type.TypeArgs[1]
		if typeArgK_kv.Name != "string" {
			t.Errorf("'Container.KV' first type arg: expected 'string', got '%s'", typeArgK_kv.Name)
		}
		if typeArgV_kv.Name != "float64" {
			t.Errorf("'Container.KV' second type arg: expected 'float64', got '%s'", typeArgV_kv.Name)
		}
		if strRep := fieldKV.Type.String(); strRep != "KeyValue[string, float64]" {
			t.Errorf("Expected String() for KV to be 'KeyValue[string, float64]', got '%s'", strRep)
		}
	})

	// --- Assertions for Generic Function Types ---
	t.Run("GenericFuncTypes", func(t *testing.T) {
		// type GenericFuncType[T any] func(T) T
		typeGenericFunc := findType(pkgInfo.Types, "GenericFuncType")
		if typeGenericFunc == nil {
			t.Fatal("Type 'GenericFuncType' not found")
		}
		if typeGenericFunc.Kind != scanner.FuncKind { // It's a type alias to a func type
			t.Errorf("Expected 'GenericFuncType' to be FuncKind, got %v", typeGenericFunc.Kind)
		}
		if len(typeGenericFunc.TypeParams) != 1 || typeGenericFunc.TypeParams[0].Name != "T" {
			t.Fatalf("Expected 1 type param 'T' for 'GenericFuncType', got %+v", typeGenericFunc.TypeParams)
		}
		if typeGenericFunc.Func == nil {
			t.Fatalf("'GenericFuncType.Func' is nil")
		}
		// Check the signature of the func type itself
		if len(typeGenericFunc.Func.Parameters) != 1 || typeGenericFunc.Func.Parameters[0].Type.Name != "T" || !typeGenericFunc.Func.Parameters[0].Type.IsTypeParam {
			t.Errorf("Parameter of 'GenericFuncType': expected 'T' (as type param), got %+v", typeGenericFunc.Func.Parameters)
		}
		if len(typeGenericFunc.Func.Results) != 1 || typeGenericFunc.Func.Results[0].Type.Name != "T" || !typeGenericFunc.Func.Results[0].Type.IsTypeParam {
			t.Errorf("Result of 'GenericFuncType': expected 'T' (as type param), got %+v", typeGenericFunc.Func.Results)
		}
	})

	// --- Assertions for Interfaces with generic methods (conceptual) or using generic types ---
	t.Run("GenericInterfaces", func(t *testing.T) {
		// type Processor[T any, U Stringer] interface { Process(data T) List[T]; ProcessKeyValue(kv KeyValue[string, T]) U }
		ifaceProcessor := findType(pkgInfo.Types, "Processor")
		if ifaceProcessor == nil {
			t.Fatal("Interface 'Processor' not found")
		}
		if ifaceProcessor.Kind != scanner.InterfaceKind {
			t.Errorf("Expected 'Processor' to be InterfaceKind, got %v", ifaceProcessor.Kind)
		}
		if len(ifaceProcessor.TypeParams) != 2 {
			t.Fatalf("Expected 2 type parameters for 'Processor', got %d", len(ifaceProcessor.TypeParams))
		} else {
			if ifaceProcessor.TypeParams[0].Name != "T" || ifaceProcessor.TypeParams[0].Constraint.Name != "any" {
				t.Errorf("Processor T param: %+v", ifaceProcessor.TypeParams[0])
			}
			if ifaceProcessor.TypeParams[1].Name != "U" || ifaceProcessor.TypeParams[1].Constraint.Name != "Stringer" {
				t.Errorf("Processor U param: %+v", ifaceProcessor.TypeParams[1])
			}
		}

		methodProcess := findMethod(ifaceProcessor.Interface, "Process")
		if methodProcess == nil {
			t.Fatal("Method 'Processor.Process' not found")
		}
		// Param: data T
		if len(methodProcess.Parameters) != 1 || methodProcess.Parameters[0].Type.Name != "T" || !methodProcess.Parameters[0].Type.IsTypeParam {
			t.Errorf("Processor.Process param: expected T, got %+v", methodProcess.Parameters)
		}
		// Result: List[T]
		if len(methodProcess.Results) != 1 {
			t.Fatal("Processor.Process expected 1 result")
		}
		resType := methodProcess.Results[0].Type
		if resType.Name != "List" || len(resType.TypeArgs) != 1 || resType.TypeArgs[0].Name != "T" || !resType.TypeArgs[0].IsTypeParam {
			t.Errorf("Processor.Process result: expected List[T], got %s args: %+v", resType.Name, resType.TypeArgs)
		}

		methodProcessKV := findMethod(ifaceProcessor.Interface, "ProcessKeyValue")
		if methodProcessKV == nil {
			t.Fatal("Method 'Processor.ProcessKeyValue' not found")
		}
		// Param: kv KeyValue[string, T]
		if len(methodProcessKV.Parameters) != 1 {
			t.Fatal("Processor.ProcessKeyValue expected 1 param")
		}
		paramKVType := methodProcessKV.Parameters[0].Type
		if paramKVType.Name != "KeyValue" || len(paramKVType.TypeArgs) != 2 {
			t.Fatalf("Processor.ProcessKeyValue param: expected KeyValue[?,?], got %s with %d args", paramKVType.Name, len(paramKVType.TypeArgs))
		}
		if paramKVType.TypeArgs[0].Name != "string" {
			t.Errorf("...arg0 not string: %+v", paramKVType.TypeArgs[0])
		}
		if paramKVType.TypeArgs[1].Name != "T" || !paramKVType.TypeArgs[1].IsTypeParam {
			t.Errorf("...arg1 not T: %+v", paramKVType.TypeArgs[1])
		}
		// Result: U
		if len(methodProcessKV.Results) != 1 || methodProcessKV.Results[0].Type.Name != "U" || !methodProcessKV.Results[0].Type.IsTypeParam {
			t.Errorf("Processor.ProcessKeyValue result: expected U, got %+v", methodProcessKV.Results)
		}
	})

	// --- Assertions for Recursive Generic Types ---
	t.Run("RecursiveGenericTypes", func(t *testing.T) {
		// type Node[T any] struct { Value T; Children []Node[T] }
		stNode := findType(pkgInfo.Types, "Node")
		if stNode == nil {
			t.Fatal("Struct 'Node' not found")
		}
		if len(stNode.TypeParams) != 1 || stNode.TypeParams[0].Name != "T" {
			t.Fatalf("Node type params: expected [T any], got %+v", stNode.TypeParams)
		}
		fieldChildren := findField(stNode.Struct, "Children")
		if fieldChildren == nil {
			t.Fatal("Node.Children field not found")
		}
		// Expected: []Node[T]
		if !fieldChildren.Type.IsSlice {
			t.Fatal("Node.Children not a slice")
		}
		elemType := fieldChildren.Type.Elem
		if elemType == nil {
			t.Fatal("Node.Children slice elem type is nil")
		}
		if elemType.Name != "Node" {
			t.Errorf("Node.Children elem type name: expected Node, got %s", elemType.Name)
		}
		if len(elemType.TypeArgs) != 1 {
			t.Fatalf("Node.Children elem type (Node) expected 1 type arg, got %d", len(elemType.TypeArgs))
		}
		typeArgNode := elemType.TypeArgs[0]
		if typeArgNode.Name != "T" || !typeArgNode.IsTypeParam {
			t.Errorf("Node.Children elem type (Node[T]) type arg: expected T, got %+v", typeArgNode)
		}
		// Check String() representation
		if strRep := fieldChildren.Type.String(); strRep != "[]Node[T]" {
			t.Errorf("Expected String() for Node.Children to be '[]Node[T]', got '%s'", strRep)
		}
	})

	// Add more assertions for other generic constructs as needed...
	// For example, methods on generic types like List[T].Add(T)
	fnListAdd := findFunction(pkgInfo, "Add") // Method names are usually unique in the function list
	if fnListAdd == nil {
		t.Fatal("Method List.Add not found")
	}
	if fnListAdd.Receiver == nil {
		t.Fatal("List.Add receiver is nil")
	}
	// Receiver: *List[T]
	recvType := fnListAdd.Receiver.Type
	if recvType.Name != "List" || !recvType.IsPointer {
		t.Errorf("List.Add receiver: expected *List, got name %s, pointer %t", recvType.Name, recvType.IsPointer)
	}
	if len(recvType.TypeArgs) != 1 {
		t.Fatalf("List.Add receiver *List[T] expected 1 type arg, got %d, args: %+v", len(recvType.TypeArgs), recvType.TypeArgs)
	} else {
		recvTypeArg := recvType.TypeArgs[0]
		if recvTypeArg.Name != "T" || !recvTypeArg.IsTypeParam {
			t.Errorf("List.Add receiver *List[T] type arg: expected T (as type param), got name '%s', isTypeParam %t. Full: %+v", recvTypeArg.Name, recvTypeArg.IsTypeParam, recvTypeArg)
		}
	}

	// Parameters: item T
	if len(fnListAdd.Parameters) != 1 {
		t.Fatalf("List.Add expected 1 parameter, got %d", len(fnListAdd.Parameters))
	}
	paramType := fnListAdd.Parameters[0].Type
	if paramType.Name != "T" || !paramType.IsTypeParam {
		t.Errorf("List.Add parameter: expected T (as type param), got name '%s', isTypeParam %t. Full: %+v", paramType.Name, paramType.IsTypeParam, paramType)
	}
}

// Helper to find a field in a struct.
func findField(sinfo *scanner.StructInfo, name string) *scanner.FieldInfo {
	if sinfo == nil {
		return nil
	}
	for _, f := range sinfo.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// Helper to find a method in an interface.
func findMethod(iinfo *scanner.InterfaceInfo, name string) *scanner.MethodInfo {
	if iinfo == nil {
		return nil
	}
	for _, m := range iinfo.Methods {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// Ensure existing TestImplements and other tests still pass by keeping their structure.
// The `findType` helper was already present.

func writeTestFiles(t *testing.T, files map[string]string) (string, func()) {
	t.Helper()
	tmpdir, err := os.MkdirTemp("", "goscan_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
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

	return tmpdir, func() { os.RemoveAll(tmpdir) }
}

func TestImplements_DerivingJSON_Scenario(t *testing.T) {
	ctx := context.Background()
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

	tmpdir, cleanup := writeTestFiles(t, files)
	defer cleanup()

	s, err := New(WithWorkDir(tmpdir))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	pkgInfo, err := s.ScanPackageFromFilePath(ctx, tmpdir)
	if err != nil {
		t.Fatalf("ScanPackageFromFilePath(%q) failed: %v", tmpdir, err)
	}

	// Find the interface
	eventDataInterface := findType(pkgInfo.Types, "EventData")
	if eventDataInterface == nil {
		t.Fatal("Interface 'EventData' not found")
	}

	// Find implementers
	var implementers []*scanner.TypeInfo
	for _, ti := range pkgInfo.Types {
		if ti.Kind == StructKind {
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
	if !reflect.DeepEqual(implementerNames, expectedImplementers) {
		t.Errorf("Expected implementers %v, got %v", expectedImplementers, implementerNames)
	}
}
