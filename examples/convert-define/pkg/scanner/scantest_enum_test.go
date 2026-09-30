package scanner_test

import (
	"context"
	"fmt"
	"testing"

	scan "github.com/podhmo/minigo/examples/convert-define/pkg/goscan"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scantest"
)

func TestEnumScanning_LazyLoaded_WithScantest(t *testing.T) {
	files := map[string]string{
		"go.mod": "module example.com/enums",
		"models/model.go": `
package models

// Status represents the status of a task.
type Status int

const (
	ToDo Status = iota
	InProgress
	Done
)
`,
		"main/main.go": `
package main
import "example.com/enums/models"
type Task struct {
	CurrentStatus models.Status
}
`,
	}

	dir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	action := func(ctx context.Context, s *scan.Scanner, pkgs []*scan.Package) error {
		if len(pkgs) != 1 {
			return fmt.Errorf("expected 1 package, but got %d", len(pkgs))
		}
		mainPkg := pkgs[0]
		if mainPkg.Name != "main" {
			return fmt.Errorf("expected package 'main', but got '%s'", mainPkg.Name)
		}

		taskType := mainPkg.Lookup("Task")
		if taskType == nil {
			return fmt.Errorf("type 'Task' not found in main package")
		}
		if taskType.Struct == nil || len(taskType.Struct.Fields) == 0 {
			return fmt.Errorf("Task struct is not parsed correctly")
		}
		statusField := taskType.Struct.Fields[0]
		if statusField.Name != "CurrentStatus" {
			return fmt.Errorf("expected first field to be 'CurrentStatus', got %s", statusField.Name)
		}

		// Here we use the top-level scanner to resolve the type.
		// This is a more realistic integration test of the scanner's full capabilities.
		resolvedType, err := s.ResolveType(ctx, statusField.Type)
		if err != nil {
			return fmt.Errorf("Resolve() for models.Status failed: %w", err)
		}

		if resolvedType == nil {
			return fmt.Errorf("expected to resolve type models.Status, but got nil")
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
		return nil
	}

	// We scan the 'main' package. The resolver inside the scanner (configured by scantest)
	// will handle finding and scanning the 'models' package on-demand.
	ctx := context.Background()
	if _, err := scantest.Run(t, ctx, dir, []string{"./main"}, action); err != nil {
		t.Fatalf("scantest.Run() failed: %v", err)
	}
}
