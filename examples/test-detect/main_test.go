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
