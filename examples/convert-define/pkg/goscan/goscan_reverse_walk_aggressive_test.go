package goscan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFindImportersAggressively(t *testing.T) {
	testDir := "../testdata/walk"

	// Git requires a user and email to be set to make commits.
	// We can use a temporary HOME directory to avoid interfering with the user's global git config.
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Set up a git repository in the test directory
	gitInitCmd := exec.Command("git", "init")
	gitInitCmd.Dir = testDir
	if err := gitInitCmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	// Clean up .git directory after test
	t.Cleanup(func() {
		os.RemoveAll(filepath.Join(testDir, ".git"))
	})

	// Configure git user
	gitConfigUserCmd := exec.Command("git", "config", "user.name", "Test User")
	gitConfigUserCmd.Dir = testDir
	if err := gitConfigUserCmd.Run(); err != nil {
		t.Fatalf("git config user.name failed: %v", err)
	}
	gitConfigEmailCmd := exec.Command("git", "config", "user.email", "test@example.com")
	gitConfigEmailCmd.Dir = testDir
	if err := gitConfigEmailCmd.Run(); err != nil {
		t.Fatalf("git config user.email failed: %v", err)
	}

	// Add and commit all files
	gitAddCmd := exec.Command("git", "add", ".")
	gitAddCmd.Dir = testDir
	if err := gitAddCmd.Run(); err != nil {
		t.Fatalf("git add failed: %v", err)
	}

	gitCommitCmd := exec.Command("git", "commit", "-m", "initial commit")
	gitCommitCmd.Dir = testDir
	if err := gitCommitCmd.Run(); err != nil {
		t.Fatalf("git commit failed: %v", err)
	}

	s, err := New(WithWorkDir(testDir))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	tests := []struct {
		name          string
		targetPackage string
		wantImporters []string
	}{
		{
			name:          "importers of c",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/c",
			wantImporters: []string{
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/a",
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/b",
			},
		},
		{
			name:          "importers of a",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/a",
			wantImporters: []string{
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/d",
			},
		},
		{
			name:          "importers of b",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/b",
			wantImporters: []string{
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/a",
			},
		},
		{
			name:          "importers of d",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/d",
			wantImporters: []string{
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/a",
			},
		},
		{
			name:          "no importers",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/e", // non-existent package
			wantImporters: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			importers, err := s.Walker.FindImportersAggressively(context.Background(), tt.targetPackage)
			if err != nil {
				t.Fatalf("FindImportersAggressively() failed: %v", err)
			}

			gotImporters := make([]string, len(importers))
			for i, pkg := range importers {
				gotImporters[i] = pkg.ImportPath
			}

			sort.Strings(gotImporters)
			sort.Strings(tt.wantImporters)

			if diff := cmp.Diff(tt.wantImporters, gotImporters); diff != "" {
				t.Errorf("mismatch importers (-want +got):\n%s", diff)
			}
		})
	}
}
