package goscan_test

import (
	"context"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	goscan "github.com/podhmo/minigo/examples/convert-define/pkg/goscan"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scantest"
)

func TestScan_Wildcard(t *testing.T) {
	files := map[string]string{
		"go.mod":           `module example.com/wildcard`,
		"main.go":          `package main`,
		"pkg/a/a.go":       `package a`,
		"pkg/a/sub/sub.go": `package sub`,
		"vendor/v/v.go":    `package v`,
		"empty/README.md":  `empty dir`,
	}
	dir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	s, err := goscan.New(goscan.WithWorkDir(dir), goscan.WithGoModuleResolver())
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	pkgs, err := s.Scan(context.Background(), "./...")
	if err != nil {
		t.Fatalf("Scan(\"./...\") failed: %v", err)
	}

	if len(pkgs) != 4 {
		t.Fatalf("expected 4 packages, but got %d", len(pkgs))
	}

	gotPkgs := make([]string, len(pkgs))
	for i, p := range pkgs {
		gotPkgs[i] = p.ImportPath
	}
	// The sort is important because the order from WalkDir is not guaranteed.
	sort.Strings(gotPkgs)

	wantPkgs := []string{
		"example.com/wildcard",
		"example.com/wildcard/pkg/a",
		"example.com/wildcard/pkg/a/sub",
		"example.com/wildcard/vendor/v",
	}
	if diff := cmp.Diff(wantPkgs, gotPkgs); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}
