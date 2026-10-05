package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunMainRejectsNonDirectoryRoot(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "notadir")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runMain(context.Background(), []string{"-root", f, "a/a.go"}); code == 0 {
		t.Errorf("-root %q (a file): want non-zero exit", f)
	}
}

func TestRunMainFollowsSymlinkedRoot(t *testing.T) {
	root := testRepo(t)
	link := filepath.Join(t.TempDir(), "rootlink")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// The walk sees a symlink as a file, so an unresolved -root link
	// used to report "no go.mod found".
	if code := runMain(context.Background(), []string{"-root", link, "a/a.go"}); code != 0 {
		t.Errorf("-root %q (symlink to repo): want exit 0, got %d", link, code)
	}
}
