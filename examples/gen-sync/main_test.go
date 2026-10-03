package main

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
)

// setupModule copies the example's go.mod, the app fixture, and the
// scanx helper into a temp module so the script's writes never touch
// the repo. scanx must be copied because the interpreted script imports
// it and the engine resolves module-local paths under the temp root.
func setupModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	copyFile(t, "go.mod", filepath.Join(dir, "go.mod"))
	copyTree(t, "app", filepath.Join(dir, "app"))
	copyTree(t, "scanx", filepath.Join(dir, "scanx"))
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

	// first run: files with stale or missing managed blocks get synced;
	// status.go and eof.go (managed block at end of file) are already in
	// sync and the decoy files stay untouched.
	n, err := run(context.Background(), dir, scriptDir(t), app, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Fatalf("expected 10 files changed, got %d", n)
	}
	for _, name := range []string{
		"level", "job", "config", "store", "events",
		"shapes", "phase", "ops", "retired", "status", "graph", "eof",
	} {
		assertSameFile(t, filepath.Join(app, name+".go"), "testdata/"+name+".golden")
	}
	// decoy files want no directives and were never written.
	assertSameFile(t, filepath.Join(app, "decoys.go"), "app/decoys.go")
	assertSameFile(t, filepath.Join(app, "phase_consts.go"), "app/phase_consts.go")

	// second run: idempotent — and ops.go's hand-written directive below
	// the inserted sentinel survives regeneration.
	n, err = run(context.Background(), dir, scriptDir(t), app, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 files changed on rerun, got %d", n)
	}
	assertSameFile(t, filepath.Join(app, "ops.go"), "testdata/ops.golden")
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
	if n != 10 {
		t.Fatalf("expected 10 drifting files, got %d", n)
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

	// with -deps, the import edges into the app/ subtree are followed
	// (mood and bound get blocks, meta is visited and left alone), while
	// the edge to scanx leaves the subtree and is never followed — the
	// tool's own helper is not a sync target.
	dir = setupModule(t)
	app = filepath.Join(dir, "app")
	n, err := run(context.Background(), dir, scriptDir(t), app, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Fatalf("expected 12 files changed with -deps, got %d", n)
	}
	assertSameFile(t, filepath.Join(app, "internal", "mood", "mood.go"), "testdata/mood.golden")
	assertSameFile(t, filepath.Join(app, "internal", "bound", "bound.go"), "testdata/bound.golden")
	assertSameFile(t, filepath.Join(app, "internal", "meta", "meta.go"), "app/internal/meta/meta.go")
	assertSameFile(t, filepath.Join(dir, "scanx", "scanx.go"), "scanx/scanx.go")
	assertSameFile(t, filepath.Join(dir, "scanx", "inspect.go"), "scanx/inspect.go")
}

func TestBoundPackage(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")

	// Bind() shadows the package path behind a host package — the way
	// bound stdlib packages answer inspect.PackageOf. Exploration still
	// enters through inspect.SourceOf, so BoundRef earns its directive
	// from bound.Marked's required field behind the shadow.
	e := minigo.NewEngine(dir, minigo.WithOutput(io.Discard))
	e.Bind("github.com/podhmo/minigo/examples/gen-sync/app/internal/bound",
		map[string]runtime.Value{"Sentinel": int64(0)})
	if _, err := e.Run(context.Background(), scriptDir(t), "Main", app, false, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(app, "graph.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "requiredgen -type=BoundRef") {
		t.Error("BoundRef did not earn requiredgen behind the bound shadow")
	}
}
