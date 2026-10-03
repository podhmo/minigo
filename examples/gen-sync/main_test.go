package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// setupModule copies the example's go.mod and the app fixture into a
// temp module so the script's writes never touch the repo.
func setupModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyFile(t, "go.mod", filepath.Join(dir, "go.mod"))
	copyTree(t, "app", filepath.Join(dir, "app"))
	return dir
}

func scriptDir(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("script")
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, src, dst string) {
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
		t.Fatal(err)
	}
}

// assertSameFile compares got's contents with the file at wantPath.
func assertSameFile(t *testing.T, got, wantPath string) {
	t.Helper()
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), string(data)); diff != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", got, diff)
	}
}

func TestSync(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")

	// first run: job.go gains a managed block, level.go's stale
	// directive is corrected, status.go is already in sync.
	n, err := run(context.Background(), dir, scriptDir(t), app, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 files changed, got %d", n)
	}
	assertSameFile(t, filepath.Join(app, "level.go"), "testdata/level.golden")
	assertSameFile(t, filepath.Join(app, "job.go"), "testdata/job.golden")
	assertSameFile(t, filepath.Join(app, "status.go"), "testdata/status.golden")

	// second run: idempotent.
	n, err = run(context.Background(), dir, scriptDir(t), app, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 files changed on rerun, got %d", n)
	}
	assertSameFile(t, filepath.Join(app, "job.go"), "testdata/job.golden")
}

func TestCheck(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")

	// check mode reports drift but writes nothing.
	n, err := run(context.Background(), dir, scriptDir(t), app, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 drifting files, got %d", n)
	}
	assertSameFile(t, filepath.Join(app, "job.go"), "app/job.go")
	assertSameFile(t, filepath.Join(app, "level.go"), "app/level.go")

	// after a real sync, check is clean.
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false); err != nil {
		t.Fatal(err)
	}
	n, err = run(context.Background(), dir, scriptDir(t), app, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 drifting files after sync, got %d", n)
	}
}

func TestDeps(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")

	// without -deps, internal/mood is not reached.
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false); err != nil {
		t.Fatal(err)
	}
	assertSameFile(t, filepath.Join(app, "internal", "mood", "mood.go"), "app/internal/mood/mood.go")

	// with -deps, the import edge app -> app/internal/mood is followed.
	dir = setupModule(t)
	app = filepath.Join(dir, "app")
	n, err := run(context.Background(), dir, scriptDir(t), app, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("expected 3 files changed with -deps, got %d", n)
	}
	assertSameFile(t, filepath.Join(app, "internal", "mood", "mood.go"), "testdata/mood.golden")
}
