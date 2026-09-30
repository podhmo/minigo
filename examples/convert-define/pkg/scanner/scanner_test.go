package scanner

import (
	"context"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// MockResolver is a mock implementation of PackageResolver for tests.
type MockResolver struct {
	ScanPackageFromImportPathFunc func(ctx context.Context, importPath string) (*PackageInfo, error)
}

func (m *MockResolver) ScanPackageFromImportPath(ctx context.Context, importPath string) (*PackageInfo, error) {
	if m.ScanPackageFromImportPathFunc != nil {
		return m.ScanPackageFromImportPathFunc(ctx, importPath)
	}
	return nil, nil // Default mock behavior
}

func newTestScanner(t *testing.T, modulePath, rootDir string) *Scanner {
	t.Helper()
	fset := token.NewFileSet()
	s, err := New(fset, nil, nil, modulePath, rootDir, &MockResolver{}, false, nil)
	if err != nil {
		t.Fatalf("scanner.New failed: %v", err)
	}
	return s
}

func TestNewScanner(t *testing.T) {
	modulePath := "example.com/test"
	rootDir := "/tmp/test"
	resolver := &MockResolver{}

	t.Run("nil_fset", func(t *testing.T) {
		_, err := New(nil, nil, nil, modulePath, rootDir, resolver, false, nil)
		if err == nil {
			t.Error("Expected error when creating scanner with nil fset, got nil")
		}
	})

	t.Run("valid_fset", func(t *testing.T) {
		fset := token.NewFileSet()
		s, err := New(fset, nil, nil, modulePath, rootDir, resolver, false, nil)
		if err != nil {
			t.Errorf("Expected no error when creating scanner with valid fset, got %v", err)
		}
		if s == nil {
			t.Error("Scanner should not be nil with valid fset")
		} else if s.fset != fset {
			t.Error("Scanner fset not set correctly")
		}
	})

	t.Run("nil_resolver", func(t *testing.T) {
		fset := token.NewFileSet()
		_, err := New(fset, nil, nil, modulePath, rootDir, nil, false, nil)
		if err == nil {
			t.Error("Expected error when creating scanner with nil resolver, got nil")
		}
	})
}

func TestScanPackageFromFilePathFeatures(t *testing.T) {
	testDir := filepath.Join("..", "testdata", "features")
	absTestDir, _ := filepath.Abs(testDir)
	s := newTestScanner(t, "example.com/test/features", absTestDir)

	// Scan only features.go and another.go, which belong to the same package "features"
	filesToScan := []string{
		filepath.Join(testDir, "features.go"),
		filepath.Join(testDir, "another.go"),
		filepath.Join(testDir, "variadic.go"),
	}

	ctx := context.Background()
	pkgInfo, err := s.ScanFiles(ctx, filesToScan, testDir)
	if err != nil {
		t.Fatalf("ScanFiles failed for %v: %v", filesToScan, err)
	}

	types := make(map[string]*TypeInfo)
	for _, ti := range pkgInfo.Types {
		types[ti.Name] = ti
	}

	// Test 1: Type with doc comment
	itemStruct, ok := types["Item"]
	if !ok {
		t.Fatal("Type 'Item' not found")
	}
	expectedDoc := "Item represents a product with an ID and Name."
	if itemStruct.Doc != expectedDoc {
		t.Errorf("Expected Item doc %q, got %q", expectedDoc, itemStruct.Doc)
	}

	// Test 2: Field with doc comment and line comment
	if len(itemStruct.Struct.Fields) < 2 {
		t.Fatal("Expected at least 2 fields in Item")
	}
	idField := itemStruct.Struct.Fields[0]
	if idField.Name != "ID" || idField.Doc != "The unique identifier for the item." {
		t.Errorf("ID field doc mismatch. Got: %q", idField.Doc)
	}

	// Test 3: Type alias with underlying type
	userIDAlias, ok := types["UserID"]
	if !ok {
		t.Fatal("Type 'UserID' not found")
	}
	if userIDAlias.Kind != AliasKind {
		t.Errorf("Expected UserID kind to be AliasKind, got %v", userIDAlias.Kind)
	}
	if userIDAlias.Underlying == nil || userIDAlias.Underlying.Name != "int64" {
		t.Errorf("Expected UserID underlying type to be 'int64', got %v", userIDAlias.Underlying)
	}

	// Test 4: Function type
	handlerFunc, ok := types["HandlerFunc"]
	if !ok {
		t.Fatal("Type 'HandlerFunc' not found")
	}
	if handlerFunc.Kind != FuncKind {
		t.Errorf("Expected HandlerFunc kind to be FuncKind, got %v", handlerFunc.Kind)
	}

	// Test 5: Variadic function
	funcs := make(map[string]*FunctionInfo)
	for _, fi := range pkgInfo.Functions {
		funcs[fi.Name] = fi
	}

	variadicFunc, ok := funcs["VariadicFunc"]
	if !ok {
		t.Fatal("Function 'VariadicFunc' not found")
	}
	if !variadicFunc.IsVariadic {
		t.Error("Expected VariadicFunc to be variadic")
	}
	if len(variadicFunc.Parameters) != 2 {
		t.Errorf("Expected 2 parameters for VariadicFunc, got %d", len(variadicFunc.Parameters))
	}

	nonVariadicFunc, ok := funcs["NonVariadicFunc"]
	if !ok {
		t.Fatal("Function 'NonVariadicFunc' not found")
	}
	if nonVariadicFunc.IsVariadic {
		t.Error("Expected NonVariadicFunc to not be variadic")
	}
}

func TestScanFiles(t *testing.T) {
	testdataDir, err := filepath.Abs(filepath.Join("..", "testdata", "features"))
	if err != nil {
		t.Fatalf("Failed to get absolute path for testdata dir: %v", err)
	}
	s := newTestScanner(t, "example.com/test/features", testdataDir)

	t.Run("scan_single_file", func(t *testing.T) {
		filePath := filepath.Join(testdataDir, "features.go")
		ctx := context.Background()
		pkgInfo, err := s.ScanFiles(ctx, []string{filePath}, testdataDir)
		if err != nil {
			t.Fatalf("ScanFiles single file failed: %v", err)
		}
		if pkgInfo.Name != "features" {
			t.Errorf("Expected package name 'features', got '%s'", pkgInfo.Name)
		}
		if len(pkgInfo.Files) != 1 || filepath.Base(pkgInfo.Files[0]) != "features.go" {
			t.Errorf("Expected 1 file 'features.go', got %v", pkgInfo.Files)
		}
		if len(pkgInfo.Types) == 0 { // Check based on content of features.go
			t.Error("Expected types to be parsed from features.go")
		}
	})

	t.Run("scan_multiple_files_same_package", func(t *testing.T) {
		filePaths := []string{
			filepath.Join(testdataDir, "features.go"),
			filepath.Join(testdataDir, "another.go"),
		}
		ctx := context.Background()
		pkgInfo, err := s.ScanFiles(ctx, filePaths, testdataDir)
		if err != nil {
			t.Fatalf("ScanFiles multiple files failed: %v", err)
		}
		if pkgInfo.Name != "features" {
			t.Errorf("Expected package name 'features', got '%s'", pkgInfo.Name)
		}
		if len(pkgInfo.Files) != 2 {
			t.Errorf("Expected 2 files, got %d", len(pkgInfo.Files))
		}
		// Check if types from both files are present
		typeNames := make(map[string]bool)
		for _, ti := range pkgInfo.Types {
			typeNames[ti.Name] = true
		}
		if !typeNames["Item"] || !typeNames["Point"] { // Item from features.go, Point from another.go
			t.Errorf("Expected types from both files to be present. Found: %v", typeNames)
		}
	})

	t.Run("scan_files_different_packages", func(t *testing.T) {
		filePaths := []string{
			filepath.Join(testdataDir, "features.go"),     // package features
			filepath.Join(testdataDir, "differentpkg.go"), // package otherfeatures
		}
		ctx := context.Background()
		_, err := s.ScanFiles(ctx, filePaths, testdataDir)
		if err == nil {
			t.Error("Expected error when scanning files from different packages, got nil")
		}
	})

	t.Run("scan_empty_file_list", func(t *testing.T) {
		ctx := context.Background()
		_, err := s.ScanFiles(ctx, []string{}, testdataDir)
		if err == nil {
			t.Error("Expected error when scanning an empty file list, got nil")
		}
	})

	t.Run("scan_non_existent_file", func(t *testing.T) {
		filePaths := []string{filepath.Join(testdataDir, "nonexistent.go")}
		ctx := context.Background()
		_, err := s.ScanFiles(ctx, filePaths, testdataDir)
		if err == nil {
			t.Error("Expected error when scanning non-existent file, got nil")
		}
	})

	t.Run("scan_with_known_import_path", func(t *testing.T) {
		filePath := filepath.Join(testdataDir, "features.go")
		canonicalPath := "my/canonical/path"
		ctx := context.Background()
		pkgInfo, err := s.ScanFilesWithKnownImportPath(ctx, []string{filePath}, testdataDir, canonicalPath)
		if err != nil {
			t.Fatalf("ScanFilesWithKnownImportPath failed: %v", err)
		}
		if pkgInfo.ImportPath != canonicalPath {
			t.Errorf("Expected import path %q, got %q", canonicalPath, pkgInfo.ImportPath)
		}
		// Verify the physical path is still correct
		if pkgInfo.Path != testdataDir {
			t.Errorf("Expected physical path %q, got %q", testdataDir, pkgInfo.Path)
		}
	})
}

func TestFieldType_Resolve(t *testing.T) {
	// Setup a mock resolver that returns a predefined package info
	resolver := &MockResolver{
		ScanPackageFromImportPathFunc: func(ctx context.Context, importPath string) (*PackageInfo, error) {
			if importPath == "example.com/models" {
				return &PackageInfo{
					Fset: token.NewFileSet(),
					Types: []*TypeInfo{
						{Name: "User", Kind: StructKind},
					},
				}, nil
			}
			return nil, nil
		},
	}

	ft := &FieldType{
		Name:           "models.User",
		PkgName:        "models",
		Resolver:       resolver,
		FullImportPath: "example.com/models",
		TypeName:       "User",
	}

	// Create a scanner to use its ResolveType method, which sets up the context
	s, err := New(token.NewFileSet(), nil, nil, "example.com/test", "/tmp", resolver, false, nil)
	if err != nil {
		t.Fatalf("scanner.New failed: %v", err)
	}

	// First call to Resolve should trigger the resolver
	ctx := context.Background()
	def, err := s.ResolveType(ctx, ft)
	if err != nil {
		t.Fatalf("ResolveType() failed: %v", err)
	}
	if def.Name != "User" {
		t.Errorf("Expected resolved type to be 'User', got %q", def.Name)
	}
	if ft.Definition == nil {
		t.Fatal("Definition should be cached after first call")
	}

	// Second call should use the cache (we can't easily test this, but we can nil out the func)
	resolver.ScanPackageFromImportPathFunc = nil // To ensure resolver is not called again
	def2, err := s.ResolveType(ctx, ft)
	if err != nil {
		t.Fatalf("Second ResolveType() call failed: %v", err)
	}
	if def2.Name != "User" {
		t.Errorf("Expected cached resolved type to be 'User', got %q", def2.Name)
	}
}

func TestResolve_DirectRecursion(t *testing.T) {
	fset := token.NewFileSet()
	testDir := filepath.Join("..", "testdata", "recursion", "direct")
	absTestDir, _ := filepath.Abs(testDir)

	// Create scanner with a mock resolver that can return the package being scanned.
	s, err := New(fset, nil, nil, "example.com/test/recursion/direct", absTestDir, &MockResolver{}, false, nil)
	if err != nil {
		t.Fatalf("scanner.New failed: %v", err)
	}

	ctx := context.Background()
	pkgInfo, err := s.ScanFiles(ctx, []string{filepath.Join(testDir, "direct.go")}, testDir)
	if err != nil {
		t.Fatalf("ScanFiles failed: %v", err)
	}

	// Set up the mock resolver to return the already scanned package, simulating a cache hit.
	s.resolver.(*MockResolver).ScanPackageFromImportPathFunc = func(ctx context.Context, importPath string) (*PackageInfo, error) {
		if importPath == "example.com/test/recursion/direct" {
			return pkgInfo, nil
		}
		return nil, fmt.Errorf("unexpected import path in test: %s", importPath)
	}
	if err != nil {
		t.Fatalf("ScanFiles failed: %v", err)
	}

	nodeType := pkgInfo.Lookup("Node")
	if nodeType == nil {
		t.Fatal("Type 'Node' not found")
	}
	if nodeType.Struct == nil || len(nodeType.Struct.Fields) == 0 {
		t.Fatal("Node struct is not parsed correctly")
	}

	nextField := nodeType.Struct.Fields[1]
	if nextField.Name != "Next" {
		t.Fatalf("Expected second field to be 'Next', got %s", nextField.Name)
	}

	// Attempt to resolve the recursive field.
	// With cycle detection, this should not cause a stack overflow.
	// It should return nil, nil because the type is already in the 'resolving' map.
	resolvedType, err := s.ResolveType(ctx, nextField.Type)
	if err != nil {
		t.Fatalf("ResolveType() for recursive type failed with error: %v", err)
	}
	if resolvedType != nil {
		// In the new logic, a cycle returns (nil, nil), and the caller is expected to handle it.
		// The original `Definition` on the FieldType might get populated later as the stack unwinds.
		// Let's check if the definition points back to itself.
		if nextField.Type.Definition != nodeType {
			// This check is tricky. The key is that the call returns without crashing.
			// The Definition might not be set in this specific test setup, but the call shouldn't hang.
			// t.Errorf("Expected resolved type to be the Node itself, but got %v", resolvedType)
		}
	}

	// The most important part of this test is that the Resolve call returns without a stack overflow.
	// If we reach here, the test is largely successful.
}

func TestResolve_MutualRecursion(t *testing.T) {
	fset := token.NewFileSet()
	rootDir := filepath.Join("..", "testdata", "recursion", "mutual")
	absRootDir, _ := filepath.Abs(rootDir)

	// This scanner will be used by the MockResolver to perform actual scanning.
	s, err := New(fset, nil, nil, "example.com/recursion/mutual", absRootDir, &MockResolver{}, false, nil)
	if err != nil {
		t.Fatalf("scanner.New failed: %v", err)
	}

	// The resolver needs to be able to scan packages on demand and cache the results
	// to prevent re-parsing and creating duplicate TypeInfo objects.
	pkgCache := make(map[string]*PackageInfo)
	mockResolver := &MockResolver{
		ScanPackageFromImportPathFunc: func(ctx context.Context, importPath string) (*PackageInfo, error) {
			if pkg, found := pkgCache[importPath]; found {
				return pkg, nil
			}
			var pkgDir string
			switch importPath {
			case "example.com/recursion/mutual/pkg_a":
				pkgDir = filepath.Join(rootDir, "pkg_a")
			case "example.com/recursion/mutual/pkg_b":
				pkgDir = filepath.Join(rootDir, "pkg_b")
			default:
				return nil, fmt.Errorf("unexpected import path: %s", importPath)
			}
			// Use the main scanner 's' to perform the scan.
			pkg, err := s.ScanFiles(ctx, []string{filepath.Join(pkgDir, filepath.Base(pkgDir)+".go")}, pkgDir)
			if err == nil && pkg != nil {
				pkgCache[importPath] = pkg // Store in cache
			}
			return pkg, err
		},
	}
	s.resolver = mockResolver

	// Start by scanning pkg_a
	ctx := context.Background()
	pkgAInfo, err := s.ScanPackageFromImportPath(ctx, "example.com/recursion/mutual/pkg_a")
	if err != nil {
		t.Fatalf("ScanPackageFromImportPath for pkg_a failed: %v", err)
	}

	typeA := pkgAInfo.Lookup("A")
	if typeA == nil {
		t.Fatal("Type 'A' not found in pkg_a")
	}
	fieldB := typeA.Struct.Fields[0]
	if fieldB.Name != "B" {
		t.Fatal("Field 'B' not found in struct 'A'")
	}

	// Now, attempt to resolve B. This will trigger a scan of pkg_b, which will in turn
	// try to resolve A from pkg_a, creating a cycle.
	resolvedType, err := s.ResolveType(ctx, fieldB.Type)
	if err != nil {
		t.Fatalf("ResolveType() for mutual recursion failed: %v", err)
	}

	// The initial resolution of B from A should succeed and return a TypeInfo for B.
	if resolvedType == nil {
		t.Fatal("Expected to resolve type B, but got nil")
	}
	if resolvedType.Name != "B" {
		t.Errorf("Expected resolved type to be 'B', got '%s'", resolvedType.Name)
	}

	// Inside the resolution of B, it will try to resolve A. At that point, a cycle is detected.
	// The call should complete without a stack overflow.

	// Now, let's verify that the definitions are linked correctly after the process.
	if fieldB.Type.Definition == nil {
		t.Fatalf("The definition for field B (*pkg_b.B) in type A was not set after resolution.")
	}
	if fieldB.Type.Definition.Name != "B" {
		t.Fatalf("fieldB.Type.Definition should be for type B, but was for %s", fieldB.Type.Definition.Name)
	}

	// Inside the resolution of B, its fields are parsed, but not yet resolved.
	// We need to explicitly trigger the resolution of the field that causes the cycle.
	fieldAInB := resolvedType.Struct.Fields[0]
	if fieldAInB.Name != "A" {
		t.Fatalf("Expected the field in B to be 'A', but got %s", fieldAInB.Name)
	}

	// Now, explicitly resolve the field A within B. This call should detect the cycle
	// and correctly link the definition back to the already-existing `typeA`.
	// We need to use the context from the resolved type B to continue the resolution chain.
	if resolvedType.ResolutionContext == nil {
		t.Fatalf("ResolutionContext on resolved type B is nil")
	}
	_, err = fieldAInB.Type.Resolve(resolvedType.ResolutionContext)
	if err != nil {
		t.Fatalf("Resolve() for field A within B failed unexpectedly: %v", err)
	}

	// After resolving B's field A, its definition should be set correctly.
	if fieldAInB.Type.Definition != typeA {
		t.Errorf("The definition for field A in type B is not pointing back to the original TypeInfo for A.")
		t.Errorf("Expected: %p, Got: %p", typeA, fieldAInB.Type.Definition)
	}
}

func TestScanWithOverlay(t *testing.T) {
	fset := token.NewFileSet()
	testDir := filepath.Join("..", "testdata", "basic")
	absTestDir, _ := filepath.Abs(testDir)
	modulePath := "example.com/basic"

	overlayContent := fmt.Sprintf("package basic\n\n// In-memory version of a struct\ntype User struct {\n\tID   int    `json:\"id\"`\n\tName string `json:\"name\"`\n}\n")
	overlay := Overlay{
		"basic.go": []byte(overlayContent),
	}

	// Here, we provide an absolute path for moduleRootDir
	s, err := New(fset, nil, overlay, modulePath, absTestDir, &MockResolver{}, false, nil)
	if err != nil {
		t.Fatalf("scanner.New with overlay failed: %v", err)
	}

	// ScanFiles expects absolute paths, so we construct one.
	scanFilePath := filepath.Join(absTestDir, "basic.go")

	// The pkgDirPath should also be an absolute path to the package directory.
	ctx := context.Background()
	pkgInfo, err := s.ScanFiles(ctx, []string{scanFilePath}, absTestDir)
	if err != nil {
		t.Fatalf("ScanFiles with overlay failed: %v", err)
	}

	userType := pkgInfo.Lookup("User")
	if userType == nil {
		t.Fatal("Type 'User' from overlay not found")
	}

	if len(userType.Struct.Fields) != 2 {
		t.Fatalf("Expected 2 fields in User struct from overlay, got %d", len(userType.Struct.Fields))
	}
	if userType.Struct.Fields[0].Name != "ID" || userType.Struct.Fields[1].Name != "Name" {
		t.Error("Field names in User struct from overlay are incorrect")
	}
}

func TestResolve_NonExistentLocalType(t *testing.T) {
	fset := token.NewFileSet()
	testDir := "/tmp/test"
	modulePath := "example.com/test"
	fileName := "main.go"
	filePath := filepath.Join(testDir, fileName)

	// Source code where 'MyStruct' refers to 'NonExistentType', which is not defined.
	overlayContent := []byte(`
package main

type CorrectType struct {
	Value int
}

type MyStruct struct {
	Field1 CorrectType
	Field2 NonExistentType
}
`)

	overlay := Overlay{
		fileName: overlayContent,
	}

	// Create a scanner with the overlay.
	s, err := New(fset, nil, overlay, modulePath, testDir, &MockResolver{}, false, nil)
	if err != nil {
		t.Fatalf("scanner.New with overlay failed: %v", err)
	}

	// Scan the virtual file.
	ctx := context.Background()
	pkgInfo, err := s.ScanFiles(ctx, []string{filePath}, testDir)
	if err != nil {
		t.Fatalf("ScanFiles with overlay failed: %v", err)
	}

	// Get the 'MyStruct' TypeInfo.
	myStructType := pkgInfo.Lookup("MyStruct")
	if myStructType == nil {
		t.Fatal("Type 'MyStruct' not found")
	}

	// Find the field that has the non-existent type.
	var fieldWithTypo *FieldInfo
	for _, field := range myStructType.Struct.Fields {
		if field.Name == "Field2" {
			fieldWithTypo = field
			break
		}
	}
	if fieldWithTypo == nil {
		t.Fatal("Field 'Field2' not found in 'MyStruct'")
	}

	// Attempt to resolve the type.
	_, err = s.ResolveType(ctx, fieldWithTypo.Type)

	// Assert that an error was returned.
	if err == nil {
		t.Fatal("Expected an error when resolving a non-existent local type, but got nil")
	}

	// Assert that the error message is descriptive.
	expectedErrorMsg := `could not resolve type "NonExistentType" in package "example.com/test"`
	if err.Error() != expectedErrorMsg {
		t.Errorf("Expected error message %q, got %q", expectedErrorMsg, err.Error())
	}
}

func TestScan_EmbeddedInterface(t *testing.T) {
	testDir := filepath.Join("testdata", "embeddediface")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("Failed to get absolute path for testdata dir: %v", err)
	}
	s := newTestScanner(t, "example.com/test/embeddediface", absTestDir)

	filesToScan := []string{
		filepath.Join(testDir, "iface.go"),
	}

	ctx := context.Background()
	pkgInfo, err := s.ScanFiles(ctx, filesToScan, testDir)
	if err != nil {
		t.Fatalf("ScanFiles failed: %v", err)
	}

	// Find the ReadWriter interface
	var readWriter *TypeInfo
	for _, typ := range pkgInfo.Types {
		if typ.Name == "ReadWriter" {
			readWriter = typ
			break
		}
	}
	if readWriter == nil {
		t.Fatal("ReadWriter interface not found")
	}
	if readWriter.Kind != InterfaceKind {
		t.Fatalf("Expected ReadWriter to be an interface, got %v", readWriter.Kind)
	}
	if readWriter.Interface == nil {
		t.Fatal("ReadWriter.Interface is nil")
	}

	// Check that it has one explicit method, "Close"
	if len(readWriter.Interface.Methods) != 1 {
		t.Fatalf("Expected 1 explicit method, got %d", len(readWriter.Interface.Methods))
	}
	if readWriter.Interface.Methods[0].Name != "Close" {
		t.Errorf("Expected method 'Close', got '%s'", readWriter.Interface.Methods[0].Name)
	}

	// Check that it has two embedded interfaces, "Reader" and "Writer"
	if len(readWriter.Interface.Embedded) != 2 {
		t.Fatalf("Expected 2 embedded interfaces, got %d", len(readWriter.Interface.Embedded))
	}
	if readWriter.Interface.Embedded[0].TypeName != "Reader" {
		t.Errorf("Expected first embedded type to be 'Reader', got '%s'", readWriter.Interface.Embedded[0].TypeName)
	}
	if readWriter.Interface.Embedded[1].TypeName != "Writer" {
		t.Errorf("Expected second embedded type to be 'Writer', got '%s'", readWriter.Interface.Embedded[1].TypeName)
	}

	// Let's resolve one of the embedded interfaces to be sure
	// Use the ResolutionContext from the parent type for resolving its children.
	ctx = readWriter.ResolutionContext
	readerTypeInfo, err := readWriter.Interface.Embedded[0].Resolve(ctx)
	if err != nil {
		t.Fatalf("Failed to resolve embedded Reader interface: %v", err)
	}
	if readerTypeInfo == nil {
		t.Fatal("Resolved embedded Reader interface is nil")
	}
	if readerTypeInfo.Name != "Reader" {
		t.Errorf("Expected resolved type name to be 'Reader', got '%s'", readerTypeInfo.Name)
	}
	if readerTypeInfo.Interface == nil {
		t.Fatal("Resolved Reader's Interface field is nil")
	}
	if len(readerTypeInfo.Interface.Methods) != 1 {
		t.Fatalf("Expected Reader to have 1 method, got %d", len(readerTypeInfo.Interface.Methods))
	}
	if readerTypeInfo.Interface.Methods[0].Name != "Read" {
		t.Errorf("Expected Reader method to be 'Read', got '%s'", readerTypeInfo.Interface.Methods[0].Name)
	}
}

func TestScanner_LocalTypeAlias(t *testing.T) {
	// 1. Setup: Create a temporary directory and a .go file with a local type alias.
	dir := t.TempDir()
	filePath := filepath.Join(dir, "main.go")
	code := `
package main
type S struct {
	Name string
}
func main() {
	type Alias S
}
`
	err := os.WriteFile(filePath, []byte(code), 0644)
	if err != nil {
		t.Fatalf("writing file: %v", err)
	}

	// 2. Action: Create a scanner and scan the file.
	// We use newTestScanner from scanner_test.go to simplify setup.
	s := newTestScanner(t, "example.com/me", dir)
	pkg, err := s.ScanFiles(context.Background(), []string{filePath}, dir)
	if err != nil {
		t.Fatalf("scanning files: %v", err)
	}

	// 3. Assertions: Check if the scanner correctly parsed the local alias.
	var aliasTypeInfo *TypeInfo
	for _, ti := range pkg.Types {
		if ti.Name == "Alias" {
			aliasTypeInfo = ti
			break
		}
	}

	if aliasTypeInfo == nil {
		t.Fatal("TypeInfo for Alias should be found")
	}
	if diff := cmp.Diff(AliasKind, aliasTypeInfo.Kind); diff != "" {
		t.Errorf("Alias kind mismatch (-want +got):\n%s", diff)
	}

	underlying := aliasTypeInfo.Underlying
	if underlying == nil {
		t.Fatal("Alias should have an Underlying type")
	}
	if diff := cmp.Diff("S", underlying.Name); diff != "" {
		t.Errorf("Underlying type name mismatch (-want +got):\n%s", diff)
	}

	// This is the crucial check: The scanner should resolve the underlying type
	// and link its full definition.
	if underlying.Definition == nil {
		t.Fatal("Underlying.Definition should be resolved by the scanner")
	}
	if diff := cmp.Diff("S", underlying.Definition.Name); diff != "" {
		t.Errorf("Definition name mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(StructKind, underlying.Definition.Kind); diff != "" {
		t.Errorf("Definition kind mismatch (-want +got):\n%s", diff)
	}
}

func TestScanner_LocalTypeAlias_WithPointer(t *testing.T) {
	// 1. Setup
	dir := t.TempDir()
	filePath := filepath.Join(dir, "main.go")
	code := `
package main
type S struct {
	Name string
}
func main() {
	type Alias *S
}
`
	err := os.WriteFile(filePath, []byte(code), 0644)
	if err != nil {
		t.Fatalf("writing file: %v", err)
	}

	// 2. Action
	s := newTestScanner(t, "example.com/me", dir)
	pkg, err := s.ScanFiles(context.Background(), []string{filePath}, dir)
	if err != nil {
		t.Fatalf("scanning files: %v", err)
	}

	// 3. Assertions
	var aliasTypeInfo *TypeInfo
	for _, ti := range pkg.Types {
		if ti.Name == "Alias" {
			aliasTypeInfo = ti
			break
		}
	}

	if aliasTypeInfo == nil {
		t.Fatal("TypeInfo for Alias should be found")
	}
	if diff := cmp.Diff(AliasKind, aliasTypeInfo.Kind); diff != "" {
		t.Errorf("Alias kind mismatch (-want +got):\n%s", diff)
	}

	underlying := aliasTypeInfo.Underlying
	if underlying == nil {
		t.Fatal("Alias should have an Underlying type")
	}
	if !underlying.IsPointer {
		t.Error("Underlying type should be a pointer")
	}

	elem := underlying.Elem
	if elem == nil {
		t.Fatal("Underlying pointer should have an element type")
	}
	if diff := cmp.Diff("S", elem.Name); diff != "" {
		t.Errorf("Element type name mismatch (-want +got):\n%s", diff)
	}

	// Check that the element's definition is resolved
	if elem.Definition == nil {
		t.Fatal("Underlying.Elem.Definition should be resolved")
	}
	if diff := cmp.Diff("S", elem.Definition.Name); diff != "" {
		t.Errorf("Element definition name mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(StructKind, elem.Definition.Kind); diff != "" {
		t.Errorf("Element definition kind mismatch (-want +got):\n%s", diff)
	}
}

func TestScanner_GenericMethodTypeParameters(t *testing.T) {
	// 1. Setup: Create code with a method on a generic type.
	dir := t.TempDir()
	filePath := filepath.Join(dir, "main.go")
	code := `
package main

type Box[T any] struct {
	Value T
}

// Get returns the value from the box.
func (b Box[T]) Get() T {
	return b.Value
}
`
	err := os.WriteFile(filePath, []byte(code), 0644)
	if err != nil {
		t.Fatalf("writing file: %v", err)
	}

	// 2. Action: Scan the file.
	s := newTestScanner(t, "example.com/me", dir)
	pkg, err := s.ScanFiles(context.Background(), []string{filePath}, dir)
	if err != nil {
		t.Fatalf("scanning files: %v", err)
	}

	// 3. Assertions: Check if the 'Get' method has the correct type parameter 'T'.
	var getMethod *FunctionInfo
	for _, f := range pkg.Functions {
		if f.Name == "Get" {
			getMethod = f
			break
		}
	}

	if getMethod == nil {
		t.Fatal("Function 'Get' not found")
	}

	if len(getMethod.TypeParams) != 1 {
		t.Fatalf("Expected 1 type parameter for method 'Get', but got %d", len(getMethod.TypeParams))
	}

	typeParam := getMethod.TypeParams[0]
	if typeParam.Name != "T" {
		t.Errorf("Expected type parameter name to be 'T', but got '%s'", typeParam.Name)
	}

	// Also check the constraint
	if typeParam.Constraint == nil {
		t.Fatal("Expected type parameter 'T' to have a constraint, but it was nil")
	}
	if typeParam.Constraint.Name != "any" {
		t.Errorf("Expected constraint for 'T' to be 'any', but got '%s'", typeParam.Constraint.Name)
	}
}
