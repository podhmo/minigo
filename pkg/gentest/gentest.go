// Package gentest is a test harness for code generators: it builds a
// temporary module, runs the generator against it, and reports every file
// the run created, changed, or deleted, so tests assert on the complete
// write set.
//
// [Run] executes the generator as an action — any func(ctx) error, such
// as a [github.com/podhmo/minigo.Engine.Run] call — and returns a
// [Result] diffed from the directory before and after:
//
//	dir := gentest.WriteFiles(t, map[string]string{"go.mod": "module example.com/m\n"})
//	res, err := gentest.Run(t, ctx, dir, func(ctx context.Context) error {
//		return generate(ctx, dir)
//	})
//	if err != nil {
//		t.Fatal(err)
//	}
//	if diff := cmp.Diff([]string{"gen.go"}, res.ChangedPaths()); diff != "" {
//		t.Errorf("write set mismatch (-want +got):\n%s", diff)
//	}
//	gentest.AssertSameFile(t, filepath.Join(dir, "gen.go"), "testdata/gen.golden")
//
// Host-side generator code can write through [WriteFile] instead of
// os.WriteFile; a test then installs a [FileWriter] such as
// [MemoryFileWriter] with [WithFileWriter] to capture the writes in memory.
// Captured writes never reach disk, so they do not appear in a [Result].
package gentest

// The design is adapted from go-scan's scantest package (see
// pkg/SOURCE.md): the action is engine-agnostic, and outputs come from a
// filesystem diff because generator writes usually cross the interpreter
// boundary, where a FileWriter cannot intercept them.

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Result is the filesystem diff a [Run] observed, keyed by path relative
// to the run directory. Only regular files are compared.
type Result struct {
	// Outputs holds files the action created (did not exist before).
	Outputs map[string][]byte
	// Modified holds files that existed before with different content now.
	Modified map[string][]byte
	// Deleted lists files the action removed, sorted.
	Deleted []string
}

// ChangedPaths returns the sorted relative paths the action created,
// modified, or deleted, so a test can assert the complete write set.
func (r *Result) ChangedPaths() []string {
	paths := make([]string, 0, len(r.Outputs)+len(r.Modified)+len(r.Deleted))
	for p := range r.Outputs {
		paths = append(paths, p)
	}
	for p := range r.Modified {
		paths = append(paths, p)
	}
	paths = append(paths, r.Deleted...)
	slices.Sort(paths)
	return paths
}

// Run snapshots dir, executes action, snapshots again, and returns the
// diff. The action's error is returned as is, with a nil Result. A
// successful run with no changes returns an empty, non-nil Result: check
// len(res.ChangedPaths()) == 0. A nil ctx means context.Background.
func Run(t *testing.T, ctx context.Context, dir string, action func(ctx context.Context) error) (*Result, error) {
	t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}

	before, err := readDir(dir)
	if err != nil {
		return nil, fmt.Errorf("gentest: snapshot before run: %w", err)
	}

	if err := action(ctx); err != nil {
		return nil, err
	}

	after, err := readDir(dir)
	if err != nil {
		return nil, fmt.Errorf("gentest: snapshot after run: %w", err)
	}

	res := &Result{
		Outputs:  map[string][]byte{},
		Modified: map[string][]byte{},
	}
	for path, content := range after {
		old, ok := before[path]
		if !ok {
			res.Outputs[path] = content
		} else if !bytes.Equal(old, content) {
			res.Modified[path] = content
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			res.Deleted = append(res.Deleted, path)
		}
	}
	slices.Sort(res.Deleted)
	return res, nil
}

// WriteFiles creates a t.TempDir populated with files, keyed by path
// relative to the directory, and returns the directory. Parent
// directories are created as needed; a write failure fails the test.
func WriteFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("WriteFile(%q): %v", path, err)
		}
	}
	return dir
}

// CopyFile copies the file src to dst, creating parent directories as
// needed. A failure fails the test.
func CopyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		t.Fatalf("WriteFile(%q): %v", dst, err)
	}
}

// CopyTree copies the directory tree src to dst, so a fixture package
// becomes part of a temporary module the generator may write into without
// touching the repository. A failure fails the test.
func CopyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0644)
	})
	if err != nil {
		t.Fatalf("CopyTree(%q, %q): %v", src, dst, err)
	}
}

// AssertSameFile reports a test error with a diff when the file at
// gotPath differs in content from the file at wantPath (typically a
// testdata golden). A file that cannot be read fails the test
// immediately.
func AssertSameFile(t *testing.T, gotPath, wantPath string) {
	t.Helper()
	got, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", gotPath, diff)
	}
}

// readDir walks root and reads every regular file, keyed by slash-
// separated path relative to root.
func readDir(root string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		files[rel] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
