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
	n, err := run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard)
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
	n, err = run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard)
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
	n, err := run(context.Background(), dir, scriptDir(t), app, true, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Fatalf("expected 10 drifting files, got %d", n)
	}
	assertSameFile(t, filepath.Join(app, "job.go"), "app/job.go")
	assertSameFile(t, filepath.Join(app, "level.go"), "app/level.go")

	// after a real sync, check is clean.
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	n, err = run(context.Background(), dir, scriptDir(t), app, true, false, io.Discard)
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
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertSameFile(t, filepath.Join(app, "internal", "mood", "mood.go"), "app/internal/mood/mood.go")

	// with -deps, the import edges into the app/ subtree are followed
	// (mood, bound, and envel get blocks, meta is visited and left
	// alone), while the edge to scanx leaves the subtree and is never
	// followed — the tool's own helper is not a sync target.
	dir = setupModule(t)
	app = filepath.Join(dir, "app")
	n, err := run(context.Background(), dir, scriptDir(t), app, false, true, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if n != 13 {
		t.Fatalf("expected 13 files changed with -deps, got %d", n)
	}
	assertSameFile(t, filepath.Join(app, "internal", "mood", "mood.go"), "testdata/mood.golden")
	assertSameFile(t, filepath.Join(app, "internal", "bound", "bound.go"), "testdata/bound.golden")
	assertSameFile(t, filepath.Join(app, "internal", "envel", "shade.go"), "testdata/shade.golden")
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

// failure modes, from docs/sketch/ja/experiment-gen-sync-errors.md:
// the tool must never report success while silently losing work.

func TestUnreadableTargetFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-000 is readable for root")
	}
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	target := filepath.Join(app, "job.go")
	if err := os.Chmod(target, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(target, 0644)

	// the file drops out of the package index silently — which also
	// drops its import edges, so sibling files would be rewritten with
	// regressed directives. The run must refuse to write at all.
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard); err == nil {
		t.Fatal("expected an error for an unreadable file, got none")
	} else if !strings.Contains(err.Error(), "job.go") {
		t.Fatalf("error does not name the unreadable file: %v", err)
	}
	// nothing was written: events.go (which would have lost variants)
	// still matches its fixture.
	assertSameFile(t, filepath.Join(app, "events.go"), "app/events.go")

	// check mode sees the same failure — a file it cannot read might be
	// hiding drift, so "clean" would be a lie.
	if _, err := run(context.Background(), dir, scriptDir(t), app, true, false, io.Discard); err == nil {
		t.Fatal("expected check mode to fail too")
	}
}

func TestWriteFailurePropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-444 is writable for root")
	}
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// status.go is already in sync (no write needed); level.go needs a
	// rewrite — make it read-only so WriteFile fails.
	target := filepath.Join(app, "level.go")
	if err := os.Chmod(target, 0444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(target, 0644)

	n, err := run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard)
	if err == nil {
		t.Fatal("expected an error for an unwritable file, got none")
	}
	if !strings.Contains(err.Error(), "level.go") {
		t.Fatalf("error does not name the unwritable file: %v", err)
	}
	// other files still synced — the failure is per-file, not a panic.
	if n == 0 {
		t.Fatal("expected other files to sync before the failure")
	}
	// the file keeps its stale directive — the reported write failed.
	assertSameFile(t, target, "app/level.go")
}

func TestWritesStayInsideScannedDir(t *testing.T) {
	dir := setupModule(t)
	// An absolute-path replace makes the locator prefer the repo's
	// tree: the script's own `.../scanx` import lands on
	// /repo/.../scanx.go, and ./scanx's index lookup (same import
	// path) answers with those files — writes would escape the dir the
	// caller named.
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	gomod = []byte(strings.Replace(string(gomod), "=> ../../", "=> "+repoRoot, 1))
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), gomod, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), dir, scriptDir(t), filepath.Join(dir, "scanx"), false, false, io.Discard); err == nil {
		t.Fatal("expected an outside-directory refusal, got nil error")
	} else if !strings.Contains(err.Error(), "outside the scanned directory") {
		t.Fatalf("unexpected error: %v", err)
	}
	// the repo's own helper is untouched.
	assertSameFile(t, "scanx/scanx.go", "scanx/scanx.go")
}

func TestOutsideModuleFails(t *testing.T) {
	dir := setupModule(t) // the engine needs a module root for its own imports
	// a package dir outside any module gets a synthetic <dir> import
	// path: references never resolve, so inference would silently
	// shrink and regressed blocks would be written.
	pkg := filepath.Join(t.TempDir(), "pkg")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("app/level.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "level.go"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), dir, scriptDir(t), pkg, false, false, io.Discard); err == nil {
		t.Fatal("expected an error for a dir outside any module")
	} else if !strings.Contains(err.Error(), "outside any Go module") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtraArgsRejected(t *testing.T) {
	// `gen-sync ./app -check` puts -check into positional args — taking
	// it silently would run a *write* where a check was meant.
	if got := runMain(context.Background(), []string{"./app", "-check"}); got != 2 {
		t.Fatalf("expected exit 2 for trailing flag-as-arg, got %d", got)
	}
	if got := runMain(context.Background(), []string{"./app", "./other"}); got != 2 {
		t.Fatalf("expected exit 2 for extra positional args, got %d", got)
	}
}

func TestCRLFSentinel(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a CRLF file already carrying a correct managed block: before the
	// fix the \r defeated the sentinel match, so a second managed block
	// was inserted above the first.
	level, err := os.ReadFile("testdata/level.golden")
	if err != nil {
		t.Fatal(err)
	}
	crlf := []byte(strings.ReplaceAll(string(level), "\n", "\r\n"))
	target := filepath.Join(app, "level.go")
	if err := os.WriteFile(target, crlf, 0644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	n, err := run(context.Background(), dir, scriptDir(t), app, false, false, &buf)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(crlf) {
		t.Fatalf("in-sync CRLF file was rewritten:\n%s", cmp.Diff(string(crlf), string(got)))
	}
	_ = n // other fixture files legitimately change; level.go must not
	if !strings.Contains(buf.String(), "level.go up to date") {
		t.Fatalf("CRLF file not recognized as in-sync:\n%s", buf.String())
	}
}

func TestCRLFPreservesLineEndings(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a CRLF file with no managed block but a decl that wants one:
	// the inserted block must join with the file's own separator, not
	// mix LF lines in.
	data, err := os.ReadFile("app/job.go")
	if err != nil {
		t.Fatal(err)
	}
	crlf := []byte(strings.ReplaceAll(string(data), "\n", "\r\n"))
	target := filepath.Join(app, "job.go")
	if err := os.WriteFile(target, crlf, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(string(got), "\r\n", ""), "\n") {
		t.Fatal("LF line endings were mixed into a CRLF file")
	}
	if !strings.Contains(string(got), "//go:generate") {
		t.Fatal("managed block was not inserted")
	}
}

func TestGeneratedFileRefused(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a file another generator owns: writing a managed block into it
	// means two tools fight over the same lines on every regen.
	content := "// Code generated by mockgen. DO NOT EDIT.\n\npackage app\n\ntype MockKind int\n\nconst (\n\tMockA MockKind = iota\n\tMockB\n)\n"
	target := filepath.Join(app, "mock_gen.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard); err == nil {
		t.Fatal("expected refusal to write into a generated file")
	} else if !strings.Contains(err.Error(), "DO NOT EDIT") {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != content {
		t.Fatal("the generated file was modified")
	}
}

func TestDriftReportNamesDirectives(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// retired.go's managed block holds a directive for a type that no
	// longer exists: the report must name the dropped line, not just a
	// count.
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "- //go:generate") {
		t.Fatalf("no dropped-directive line in output:\n%s", out)
	}
	if !strings.Contains(out, "dropped") {
		t.Fatalf("no dropped count in output:\n%s", out)
	}
}

func TestDuplicateDirectiveReported(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a doubled directive inside the managed run is removed by the
	// rewrite: the diff must show the removed copy, not silently lose it.
	content := "package app\n\n// Code generated directives below are managed by gen-sync. DO NOT EDIT.\n//go:generate stringer -type=Dup\n//go:generate stringer -type=Dup\n\ntype Dup int\n\nconst DupA Dup = 1\n"
	target := filepath.Join(app, "dup.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "- //go:generate stringer -type=Dup") {
		t.Fatalf("the removed duplicate is missing from the diff:\n%s", out)
	}
	if !strings.Contains(out, "dropped 1") {
		t.Fatalf("the removed duplicate is missing from the count:\n%s", out)
	}
	got, _ := os.ReadFile(target)
	if strings.Count(string(got), "//go:generate stringer -type=Dup") != 1 {
		t.Fatal("the duplicate directive was not removed")
	}
}

func TestMixedEOLKeepsUntouchedLines(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a mixed-EOL file — CRLF header and managed run, LF decls. The
	// rewrite must only re-render the managed region; LF lines outside
	// it keep their endings.
	content := "package app\r\n\r\n// Code generated directives below are managed by gen-sync. DO NOT EDIT.\r\n//go:generate stringer -type=Old\r\n\r\ntype New int\n\nconst NewA New = 1\n"
	target := filepath.Join(app, "mixed.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, "//go:generate stringer -type=New\r\n") {
		t.Fatalf("managed region did not take the file's dominant separator:\n%q", s)
	}
	if !strings.Contains(s, "type New int\n") || strings.Contains(s, "type New int\r\n") {
		t.Fatalf("untouched LF lines were rewritten as CRLF:\n%q", s)
	}
	if strings.Contains(s, "const NewA New = 1\r\n") {
		t.Fatalf("untouched LF lines were rewritten as CRLF:\n%q", s)
	}
}

func TestMalformedConstraintFails(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// go/build drops a file whose //go:build line does not parse — the
	// same silent exclusion a valid constraint causes, but the file is
	// broken, not excluded: its decls vanish, so the run must fail.
	content := "//go:build ((broken\n\npackage app\n\ntype Broken struct{}\n"
	target := filepath.Join(app, "broken.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := run(context.Background(), dir, scriptDir(t), app, false, false, io.Discard)
	if err == nil {
		t.Fatal("expected an error for a file the build system could not parse")
	}
	if !strings.Contains(err.Error(), "broken.go") {
		t.Fatalf("error does not name the broken file: %v", err)
	}
}
