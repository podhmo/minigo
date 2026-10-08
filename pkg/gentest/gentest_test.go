package gentest_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/pkg/gentest"
)

// TestRunReportsTheWriteSet walks the harness's main loop: build a temp
// module, run an action that behaves like a generator, assert on every
// file it touched — not just the ones the test remembered to check.
func TestRunReportsTheWriteSet(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{
		"go.mod":   "module example.com/m\n\ngo 1.26\n",
		"gen.go":   "package m\n",
		"stale.go": "package m\n\n// stale content\n",
		"gone.go":  "package m\n",
	})

	res, err := gentest.Run(t, context.Background(), dir, func(ctx context.Context) error {
		// a pretend generator: creates one file, rewrites another,
		// removes a third; go.mod and gen.go stay untouched.
		if err := os.WriteFile(filepath.Join(dir, "generated.go"), []byte("package m\n\n// generated\n"), 0644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "stale.go"), []byte("package m\n\n// synced\n"), 0644); err != nil {
			return err
		}
		return os.Remove(filepath.Join(dir, "gone.go"))
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"generated.go", "gone.go", "stale.go"}
	if diff := cmp.Diff(want, res.ChangedPaths()); diff != "" {
		t.Errorf("changed paths mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("package m\n\n// generated\n", string(res.Outputs["generated.go"])); diff != "" {
		t.Errorf("created content mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("package m\n\n// synced\n", string(res.Modified["stale.go"])); diff != "" {
		t.Errorf("modified content mismatch (-want +got):\n%s", diff)
	}
}

// TestRunCleanRunReturnsEmptyResult: a run that changes nothing still
// returns a Result — "no outputs" is a fact, not a missing value.
func TestRunCleanRunReturnsEmptyResult(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{"go.mod": "module example.com/m\n"})
	res, err := gentest.Run(t, context.Background(), dir, func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || len(res.ChangedPaths()) != 0 {
		t.Fatalf("expected an empty result for a clean run, got %+v", res)
	}
}

// TestRunPropagatesActionError: the tool's own failure is returned
// untouched — the harness adds no blame of its own.
func TestRunPropagatesActionError(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{"go.mod": "module example.com/m\n"})
	sentinel := errors.New("gen-sync: something failed")
	res, err := gentest.Run(t, context.Background(), dir, func(ctx context.Context) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the action's error, got %v", err)
	}
	if res != nil {
		t.Fatalf("a failed run should not report outputs: %+v", res)
	}
}

// TestFileWriterCapturesOutputs exercises the FileWriter seam: writes
// routed through gentest.WriteFile land in the MemoryFileWriter and
// never touch disk, so Run's filesystem diff stays empty.
func TestFileWriterCapturesOutputs(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{"go.mod": "module example.com/m\n"})
	mem := &gentest.MemoryFileWriter{BaseDir: dir}
	ctx := gentest.WithFileWriter(context.Background(), mem)

	res, err := gentest.Run(t, ctx, dir, func(ctx context.Context) error {
		return gentest.WriteFile(ctx, filepath.Join(dir, "generated.go"), []byte("package m\n"), 0644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := mem.Outputs["generated.go"]; string(got) != "package m\n" {
		t.Fatalf("write was not captured: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "generated.go")); !os.IsNotExist(err) {
		t.Fatal("captured write reached the filesystem")
	}
	if len(res.ChangedPaths()) != 0 {
		t.Fatalf("intercepted writes should leave the fs-diff empty: %+v", res)
	}
}

// TestFileWriterDefaultsToDisk: without an installed writer the same
// call is plain os.WriteFile — production behavior unchanged.
func TestFileWriterDefaultsToDisk(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{"go.mod": "module example.com/m\n"})
	target := filepath.Join(dir, "generated.go")
	if err := gentest.WriteFile(context.Background(), target, []byte("package m\n"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package m\n" {
		t.Fatalf("unexpected content: %q", data)
	}
}

// TestFileWriterFuncInjectsFailures: a FileWriterFunc refuses one path,
// the way a test simulates a write failure without chmod games.
func TestFileWriterFuncInjectsFailures(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{"go.mod": "module example.com/m\n"})
	target := filepath.Join(dir, "generated.go")
	sentinel := errors.New("simulated write failure")
	w := gentest.FileWriterFunc(func(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
		return sentinel
	})
	ctx := gentest.WithFileWriter(context.Background(), w)
	if err := gentest.WriteFile(ctx, target, []byte("package m\n"), 0644); !errors.Is(err, sentinel) {
		t.Fatalf("expected the injected failure, got %v", err)
	}
}

func TestAssertSameFile(t *testing.T) {
	dir := gentest.WriteFiles(t, map[string]string{"got.go": "package m\n"})
	golden := filepath.Join(t.TempDir(), "want.golden")
	if err := os.WriteFile(golden, []byte("package m\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gentest.AssertSameFile(t, filepath.Join(dir, "got.go"), golden)
}

func TestCopyTree(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.go"), []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "fixture")
	gentest.CopyTree(t, src, dst)
	gentest.AssertSameFile(t, filepath.Join(dst, "a.go"), filepath.Join(src, "a.go"))
	gentest.AssertSameFile(t, filepath.Join(dst, "sub", "b.go"), filepath.Join(src, "sub", "b.go"))
}
