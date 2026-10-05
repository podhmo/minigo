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

func TestRunMainRejectsFlagAfterFileArg(t *testing.T) {
	code := runMain(context.Background(), []string{"a/a.go", "-format", "space"})
	if code != 2 {
		t.Errorf("flag after file arg: want exit 2, got %d", code)
	}
	// -- exempts: a leading-dash file name is then genuinely positional
	// (resolves to this package's directory and exits 0).
	code = runMain(context.Background(), []string{"--", "-weird.go"})
	if code != 0 {
		t.Errorf("-- -weird.go should reach resolution (exit 0), got %d", code)
	}
	// But -- does not make a real flag name into a file: these must
	// still fail loudly instead of silently ignoring the flag.
	for _, args := range [][]string{
		{"t.json", "--", "-format", "json"},
		{"a/a.go", "--", "-format", "json"},
		{"a/a.go", "--", "-format=json"},
		{"a/a.go", "--", "-h"},
	} {
		if code := runMain(context.Background(), args); code != 2 {
			t.Errorf("%v: want exit 2, got %d", args, code)
		}
	}
}

func TestRunMainRejectsUnknownOnUnresolved(t *testing.T) {
	if code := runMain(context.Background(), []string{"-on-unresolved", "bogus", "a/a.go"}); code != 2 {
		t.Errorf("-on-unresolved bogus: want exit 2, got %d", code)
	}
}
