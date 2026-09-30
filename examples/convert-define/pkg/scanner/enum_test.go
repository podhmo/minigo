package scanner

import (
	"context"
	"fmt"
	"go/token"
	"path/filepath"
	"testing"
)

func TestEnumScanning_PackageLevel(t *testing.T) {
	testDir := filepath.Join("..", "testdata", "enums", "models")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("could not get absolute path for test dir: %v", err)
	}

	// Use the existing newTestScanner helper
	s := newTestScanner(t, "example.com/enums", absTestDir)

	filesToScan := []string{
		filepath.Join(testDir, "model.go"),
	}

	// The dir path for ScanFiles should be the package's directory
	ctx := context.Background()
	pkgInfo, err := s.ScanFiles(ctx, filesToScan, absTestDir)
	if err != nil {
		t.Fatalf("ScanFiles failed: %v", err)
	}

	// Test for Status enum (int-based)
	statusType := pkgInfo.Lookup("Status")
	if statusType == nil {
		t.Fatal("Type 'Status' not found")
	}

	if !statusType.IsEnum {
		t.Error("Expected Status.IsEnum to be true, but it was false")
	}

	expectedMembers := map[string]bool{"ToDo": true, "InProgress": true, "Done": true}
	if len(statusType.EnumMembers) != len(expectedMembers) {
		t.Errorf("Expected %d enum members for Status, but got %d", len(expectedMembers), len(statusType.EnumMembers))
	}

	for _, member := range statusType.EnumMembers {
		if _, ok := expectedMembers[member.Name]; !ok {
			t.Errorf("Unexpected enum member found: %s", member.Name)
		}
	}

	// Test for Priority enum (string-based)
	priorityType := pkgInfo.Lookup("Priority")
	if priorityType == nil {
		t.Fatal("Type 'Priority' not found")
	}

	if !priorityType.IsEnum {
		t.Error("Expected Priority.IsEnum to be true, but it was false")
	}

	expectedPriorityMembers := map[string]bool{"Low": true, "High": true}
	if len(priorityType.EnumMembers) != len(expectedPriorityMembers) {
		t.Errorf("Expected %d enum members for Priority, but got %d", len(expectedPriorityMembers), len(priorityType.EnumMembers))
	}
}

func TestEnumScanning_LazyLoaded(t *testing.T) {
	ctx := context.Background()
	rootDir := filepath.Join("..", "testdata", "enums")
	absRootDir, err := filepath.Abs(rootDir)
	if err != nil {
		t.Fatalf("could not get absolute path for root dir: %v", err)
	}

	// This scanner will be used by the MockResolver to perform the actual scanning.
	s, err := New(token.NewFileSet(), nil, nil, "example.com/enums", absRootDir, &MockResolver{}, false, nil)
	if err != nil {
		t.Fatalf("scanner.New failed: %v", err)
	}

	pkgCache := make(map[string]*PackageInfo)
	mockResolver := &MockResolver{
		ScanPackageFromImportPathFunc: func(ctx context.Context, importPath string) (*PackageInfo, error) {
			if pkg, found := pkgCache[importPath]; found {
				return pkg, nil
			}
			var pkgDir string
			var files []string
			switch importPath {
			case "example.com/enums/models":
				pkgDir = filepath.Join(absRootDir, "models")
				files = []string{filepath.Join(pkgDir, "model.go")}
			default:
				return nil, fmt.Errorf("unexpected import path: %s", importPath)
			}

			// The scanner needs to know the canonical import path for the package it's scanning
			pkg, err := s.ScanFilesWithKnownImportPath(ctx, files, pkgDir, importPath)
			if err == nil && pkg != nil {
				pkgCache[importPath] = pkg
			}
			return pkg, err
		},
	}
	s.resolver = mockResolver

	// 1. Scan the 'main' package, which depends on the 'models' package.
	mainPkgDir := filepath.Join(absRootDir, "main")
	mainPkgFiles := []string{filepath.Join(mainPkgDir, "main.go")}
	mainPkgInfo, err := s.ScanFiles(ctx, mainPkgFiles, mainPkgDir)
	if err != nil {
		t.Fatalf("ScanFiles for main package failed: %v", err)
	}

	// 2. Find the Task struct and its 'CurrentStatus' field.
	taskType := mainPkgInfo.Lookup("Task")
	if taskType == nil {
		t.Fatal("Type 'Task' not found in main package")
	}
	if taskType.Struct == nil || len(taskType.Struct.Fields) < 2 {
		t.Fatalf("Task struct is not parsed correctly, expected 2 fields, got %d", len(taskType.Struct.Fields))
	}
	statusField := taskType.Struct.Fields[0]
	if statusField.Name != "CurrentStatus" {
		t.Fatalf("Expected first field to be 'CurrentStatus', got %s", statusField.Name)
	}

	// 3. Resolve the field's type. This should trigger the lazy-loading of the 'models' package.
	resolvedType, err := s.ResolveType(ctx, statusField.Type)
	if err != nil {
		t.Fatalf("ResolveType() for models.Status failed: %v", err)
	}

	// 4. Assert that the resolved type has the correct enum information.
	if resolvedType == nil {
		t.Fatal("Expected to resolve type models.Status, but got nil")
	}
	if resolvedType.Name != "Status" {
		t.Errorf("Expected resolved type name to be 'Status', got '%s'", resolvedType.Name)
	}

	if !resolvedType.IsEnum {
		t.Error("Expected resolved Status type IsEnum to be true, but it was false")
	}

	if len(resolvedType.EnumMembers) != 3 {
		t.Errorf("Expected 3 enum members for Status, but got %d", len(resolvedType.EnumMembers))
	}
}
