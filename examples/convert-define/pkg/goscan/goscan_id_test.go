package goscan_test

import (
	"context"
	"path/filepath"
	"testing"

	goscan "github.com/podhmo/minigo/examples/convert-define/pkg/goscan"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scantest"
)

func TestPackageID_SimpleModule(t *testing.T) {
	files := map[string]string{
		"go.mod": "module example.com/simple\n\ngo 1.21",
		"lib/lib.go": `
			package lib
			func Hello() {}
		`,
		"cmd/app/main.go": `
			package main
			func main() {}
		`,
	}
	tmpdir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	s, err := goscan.New(goscan.WithWorkDir(tmpdir), goscan.WithGoModuleResolver())
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	t.Run("standard library package", func(t *testing.T) {
		pkg, err := s.ScanPackageFromImportPath(context.Background(), "example.com/simple/lib")
		if err != nil {
			t.Fatalf("ScanPackageFromImportPath failed: %v", err)
		}
		const expectedID = "example.com/simple/lib"
		if pkg.ID != expectedID {
			t.Errorf("ID mismatch: got %q, want %q", pkg.ID, expectedID)
		}
		if pkg.ImportPath != expectedID {
			t.Errorf("ImportPath mismatch: got %q, want %q", pkg.ImportPath, expectedID)
		}
	})

	t.Run("main package", func(t *testing.T) {
		pkg, err := s.ScanPackageFromImportPath(context.Background(), "example.com/simple/cmd/app")
		if err != nil {
			t.Fatalf("ScanPackageFromImportPath failed: %v", err)
		}
		const expectedID = "example.com/simple/cmd/app.main"
		const expectedImportPath = "example.com/simple/cmd/app"
		if pkg.ID != expectedID {
			t.Errorf("ID mismatch: got %q, want %q", pkg.ID, expectedID)
		}
		if pkg.ImportPath != expectedImportPath {
			t.Errorf("ImportPath mismatch: got %q, want %q", pkg.ImportPath, expectedImportPath)
		}
	})
}

func TestPackageID_Workspace(t *testing.T) {
	files := map[string]string{
		"app1/go.mod": "module example.com/workspace/app1\n\ngo 1.21",
		"app1/main.go": `
			package main
			func main() {}
		`,
		"app2/go.mod": "module example.com/workspace/app2\n\ngo 1.21",
		"app2/main.go": `
			package main
			func main() {}
		`,
		"go.work": `
			go 1.21
			use (
				./app1
				./app2
			)
		`,
	}
	tmpdir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	scannerOptions := []goscan.ScannerOption{
		goscan.WithWorkDir(tmpdir),
		goscan.WithGoModuleResolver(),
		goscan.WithModuleDirs([]string{
			filepath.Join(tmpdir, "app1"),
			filepath.Join(tmpdir, "app2"),
		}),
	}
	s, err := goscan.New(scannerOptions...)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	t.Run("first main package", func(t *testing.T) {
		pkg, err := s.ScanPackageFromImportPath(context.Background(), "example.com/workspace/app1")
		if err != nil {
			t.Fatalf("ScanPackageFromImportPath for app1 failed: %v", err)
		}
		const expectedID = "example.com/workspace/app1.main"
		if pkg.ID != expectedID {
			t.Errorf("app1 ID mismatch: got %q, want %q", pkg.ID, expectedID)
		}
	})

	t.Run("second main package", func(t *testing.T) {
		pkg, err := s.ScanPackageFromImportPath(context.Background(), "example.com/workspace/app2")
		if err != nil {
			t.Fatalf("ScanPackageFromImportPath for app2 failed: %v", err)
		}
		const expectedID = "example.com/workspace/app2.main"
		if pkg.ID != expectedID {
			t.Errorf("app2 ID mismatch: got %q, want %q", pkg.ID, expectedID)
		}
	})
}

func TestPackageID_Replace(t *testing.T) {
	files := map[string]string{
		"libs/util/go.mod": "module example.com/util\n\ngo 1.21",
		"libs/util/helper/helper.go": `
			package helper
			func Help() {}
		`,
		"mainmodule/go.mod": `
			module example.com/mainmodule
			go 1.21
			require "example.com/util" v0.0.0
			replace "example.com/util" => "../libs/util"
		`,
		"mainmodule/main.go": `
			package main
			import _ "example.com/util/helper"
			func main() {}
		`,
	}
	tmpdir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	mainModuleDir := filepath.Join(tmpdir, "mainmodule")
	utilModuleDir, err := filepath.Abs(filepath.Join(tmpdir, "libs/util"))
	if err != nil {
		t.Fatalf("could not get absolute path for util module: %v", err)
	}

	// Create an overlay for go.mod to handle the relative path in the replace directive.
	// This makes the test robust without depending on complex `go list` behavior with relative paths.
	// In a go.mod file, local replacement paths are NOT quoted.
	goModOverlayContent := `module example.com/mainmodule
go 1.21
require "example.com/util" v0.0.0
replace example.com/util => ` + utilModuleDir + `
`
	overlay := scanner.Overlay{
		"go.mod": []byte(goModOverlayContent),
	}

	s, err := goscan.New(
		goscan.WithWorkDir(mainModuleDir),
		goscan.WithGoModuleResolver(),
		goscan.WithOverlay(overlay),
	)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	pkg, err := s.ScanPackageFromImportPath(context.Background(), "example.com/util/helper")
	if err != nil {
		t.Fatalf("ScanPackageFromImportPath failed: %v", err)
	}
	const expectedID = "example.com/util/helper"
	if pkg.ID != expectedID {
		t.Errorf("ID mismatch: got %q, want %q", pkg.ID, expectedID)
	}
}
