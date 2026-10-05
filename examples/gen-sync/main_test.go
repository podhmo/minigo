package main

import (
	"context"
	"errors"
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
	n, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, io.Discard)
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
	n, err = run(context.Background(), dir, scriptDir(t), app, false, false, false, io.Discard)
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
	n, err := run(context.Background(), dir, scriptDir(t), app, true, false, false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Fatalf("expected 10 drifting files, got %d", n)
	}
	assertSameFile(t, filepath.Join(app, "job.go"), "app/job.go")
	assertSameFile(t, filepath.Join(app, "level.go"), "app/level.go")

	// after a real sync, check is clean.
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	n, err = run(context.Background(), dir, scriptDir(t), app, true, false, false, io.Discard)
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
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertSameFile(t, filepath.Join(app, "internal", "mood", "mood.go"), "app/internal/mood/mood.go")

	// with -deps, the import edges into the app/ subtree are followed
	// (mood, bound, and envel get blocks, meta is visited and left
	// alone), while the edge to scanx leaves the subtree and is never
	// followed — the tool's own helper is not a sync target.
	dir = setupModule(t)
	app = filepath.Join(dir, "app")
	n, err := run(context.Background(), dir, scriptDir(t), app, false, true, false, io.Discard)
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
	if _, err := e.Run(context.Background(), scriptDir(t), "Main", app, false, false, false); err != nil {
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
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, io.Discard); err == nil {
		t.Fatal("expected an error for an unreadable file, got none")
	} else if !strings.Contains(err.Error(), "job.go") {
		t.Fatalf("error does not name the unreadable file: %v", err)
	}
	// nothing was written: events.go (which would have lost variants)
	// still matches its fixture.
	assertSameFile(t, filepath.Join(app, "events.go"), "app/events.go")

	// check mode sees the same failure — a file it cannot read might be
	// hiding drift, so "clean" would be a lie.
	if _, err := run(context.Background(), dir, scriptDir(t), app, true, false, false, io.Discard); err == nil {
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

	n, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, io.Discard)
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
	if _, err := run(context.Background(), dir, scriptDir(t), filepath.Join(dir, "scanx"), false, false, false, io.Discard); err == nil {
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
	if _, err := run(context.Background(), dir, scriptDir(t), pkg, false, false, false, io.Discard); err == nil {
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
	n, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf)
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
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, io.Discard); err != nil {
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

func TestGeneratedFileSkipped(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a file another generator owns: writing a managed block into it
	// means two tools fight over the same lines on every regen — skip
	// it with a warning instead of failing the whole run (a package
	// containing generated files is the normal case).
	content := "// Code generated by mockgen. DO NOT EDIT.\n\npackage app\n\ntype MockKind int\n\nconst (\n\tMockA MockKind = iota\n\tMockB\n)\n"
	target := filepath.Join(app, "mock_gen.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
		t.Fatalf("a generated file must not fail the run: %v", err)
	}
	if !strings.Contains(buf.String(), "mock_gen.go") || !strings.Contains(buf.String(), "skipping") {
		t.Fatalf("no skip warning for the generated file:\n%s", buf.String())
	}
	got, _ := os.ReadFile(target)
	if string(got) != content {
		t.Fatal("the generated file was modified")
	}
}

func TestUnderscoreDotFilesIgnored(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// Go ignores _- and .-prefixed files entirely: they are not
	// "excluded from the index", they are invisible — no warning, no
	// error, and no write refusal.
	for _, name := range []string{"_skip.go", ".hidden.go"} {
		content := "package app\n\ntype Disabled int\n"
		if err := os.WriteFile(filepath.Join(app, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
		t.Fatalf("_/.-prefixed files must not fail the run: %v", err)
	}
	if strings.Contains(buf.String(), "_skip.go") || strings.Contains(buf.String(), ".hidden.go") {
		t.Fatalf("_/.-prefixed files should be ignored, not mentioned:\n%s", buf.String())
	}
}

func TestDriftReportNamesDirectives(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// retired.go's managed block holds a directive for a type that no
	// longer exists: the report must name the dropped line, not just a
	// count.
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
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
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
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
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, io.Discard); err != nil {
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

func TestExcludedFileWarns(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// go/build drops a file when its //go:build line excludes it (or
	// fails to parse) — the same silent exclusion path in both cases.
	// The file is surfaced as a warning and the run continues; the
	// decl loss is visible in the output even though go/build itself
	// reports nothing.
	content := "//go:build ((broken\n\npackage app\n\ntype Broken struct{}\n"
	target := filepath.Join(app, "broken.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
		t.Fatalf("an excluded file must not fail the run: %v", err)
	}
	if !strings.Contains(buf.String(), "broken.go") || !strings.Contains(buf.String(), "not in the package index") {
		t.Fatalf("no exclusion warning for the dropped file:\n%s", buf.String())
	}
}

func TestForeignPackageSkipped(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a file declaring a different package clause is part of a
	// different package — go build would reject the whole directory
	// ("found packages app and otherpkg"). The index merges it anyway,
	// so the tool must warn and skip: no managed block written, and its
	// decls must not feed inference.
	content := "package otherpkg\n\ntype Foreign int\n\nconst (\n\tForeignA Foreign = iota\n\tForeignB\n)\n"
	target := filepath.Join(app, "foreign.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
		t.Fatalf("a foreign-package file must not fail the run: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "foreign.go") || !strings.Contains(out, "declares package otherpkg") {
		t.Fatalf("no foreign-package warning:\n%s", out)
	}
	got, _ := os.ReadFile(target)
	if string(got) != content {
		t.Fatal("the foreign-package file was modified")
	}
	// its decls must not leak into sibling inference: Foreign's enum
	// members stay in otherpkg, so no directive may name it.
	if strings.Contains(out, "-type=Foreign") {
		t.Fatalf("a foreign decl fed inference:\n%s", out)
	}
}

func TestForeignPackageMethodFeedsNothing(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a method declared inside a foreign-package file still lands in
	// the index's method table — before the fix it earned the type a
	// oneofgen directive AND a spot in Envelope's -variants=. The
	// skipped file must feed no inference channel.
	if err := os.WriteFile(filepath.Join(app, "squatter.go"),
		[]byte("package app\n\ntype Squatter int\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "foreign.go"),
		[]byte("package otherpkg\n\nfunc (Squatter) Discriminator() string { return \"foreign\" }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "declares package otherpkg") {
		t.Fatalf("no foreign-package warning:\n%s", out)
	}
	if strings.Contains(out, "Squatter") {
		t.Fatalf("a foreign-declared method fed inference:\n%s", out)
	}
	got, _ := os.ReadFile(filepath.Join(app, "squatter.go"))
	if strings.Contains(string(got), "//go:generate") {
		t.Fatalf("squatter.go earned a directive from a foreign method:\n%s", got)
	}
	// Envelope's -variants= must not list it either — the foreign
	// method alone made Squatter an implementer.
	events, _ := os.ReadFile(filepath.Join(app, "events.go"))
	if strings.Contains(string(events), "Squatter") {
		t.Fatalf("a foreign-declared method put Squatter in -variants=:\n%s", events)
	}
}

func TestForeignPackageDepDeclsHidden(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a dep package whose required-bearing decl lives in a
	// foreign-package file: Explorer lookups must not resolve it, so
	// the struct field reaching it earns no requiredgen.
	dep := filepath.Join(app, "internal", "reqdep")
	if err := os.MkdirAll(dep, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dep, "dep.go"), []byte("package reqdep\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dep, "foreign.go"),
		[]byte("package otherpkg\n\ntype Hidden struct {\n\tX string `required:\"true\"`\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	uses := "package app\n\nimport \"github.com/podhmo/minigo/examples/gen-sync/app/internal/reqdep\"\n\n" +
		"// UsesDep reaches reqdep.Hidden through a field — a decl that\n" +
		"// exists only in the dep's skipped foreign file.\n" +
		"type UsesDep struct {\n\tR reqdep.Hidden\n}\n"
	target := filepath.Join(app, "usesdep.go")
	if err := os.WriteFile(target, []byte(uses), 0644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "UsesDep") || strings.Contains(buf.String(), "Hidden") {
		t.Fatalf("a foreign decl fed the exploration:\n%s", buf.String())
	}
	got, _ := os.ReadFile(target)
	if strings.Contains(string(got), "//go:generate") {
		t.Fatalf("usesdep.go earned a directive through a foreign decl:\n%s", got)
	}
}

func TestInvisibleOnlyFilesNotConstraintBlamed(t *testing.T) {
	dir := setupModule(t)
	// a dir whose only .go-looking file is _-prefixed is empty to the
	// Go build system — "excluded by build constraints" is the wrong
	// reason (the file is invisible by name, not by constraint).
	pkg := filepath.Join(dir, "onlyhidden")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "_skip.go"), []byte("package onlyhidden\n\ntype S int\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := run(context.Background(), dir, scriptDir(t), pkg, false, false, false, io.Discard)
	if err == nil {
		t.Fatal("expected a no-buildable error for a name-invisible-only dir")
	}
	if !strings.Contains(err.Error(), "no buildable Go source files") {
		t.Fatalf("expected a no-buildable error, got: %v", err)
	}
	if strings.Contains(err.Error(), "build constraints") {
		t.Fatalf("name-invisible files blamed on build constraints: %v", err)
	}
}

func TestHandwrittenManagedLineDropAnnounced(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a hand-written directive placed inside the managed run is the
	// tool's to remove — but the removal must be announced, not silent:
	// the diff shows it as a dropped line (the run belongs to gen-sync;
	// the line moves outside the run or it is deleted).
	content := "package app\n\n// Code generated directives below are managed by gen-sync. DO NOT EDIT.\n//go:generate stringer -type=Dup\n//go:generate handtool -x\n\ntype Dup int\n\nconst DupA Dup = 1\n"
	target := filepath.Join(app, "dup.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "- //go:generate handtool -x") {
		t.Fatalf("the dropped hand-written line was not announced:\n%s", out)
	}
	if !strings.Contains(out, "dropped 1") {
		t.Fatalf("the dropped line is missing from the count:\n%s", out)
	}
	got, _ := os.ReadFile(target)
	if strings.Contains(string(got), "handtool") {
		t.Fatal("the hand-written directive inside the managed run survived")
	}
	if !strings.Contains(string(got), "//go:generate stringer -type=Dup") {
		t.Fatal("the inferred directive was lost")
	}
}

// setupRunnable is setupModule plus the tool's own script, and moves
// the test's working directory into the module so runMain can be
// exercised end to end (its "./script" and "." are resolved from CWD).
func setupRunnable(t *testing.T) string {
	t.Helper()
	dir := setupModule(t)
	copyTree(t, "script", filepath.Join(dir, "script"))
	t.Chdir(dir)
	return dir
}

func TestCheckExitCodes(t *testing.T) {
	// -check reserves exit 1 for "drift found"; a run that could not
	// see or write the whole picture exits 2 instead.
	dir := setupRunnable(t)

	// drift: exit 1, nothing written.
	if got := runMain(context.Background(), []string{"-check", "./app"}); got != 1 {
		t.Fatalf("expected exit 1 for drift, got %d", got)
	}
	assertSameFile(t, filepath.Join(dir, "app", "job.go"), "app/job.go")

	// a real failure — an unreadable input file — exits 2.
	target := filepath.Join(dir, "app", "job.go")
	if err := os.Chmod(target, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(target, 0644)
	if os.Geteuid() == 0 {
		t.Skip("chmod-000 is readable for root")
	}
	if got := runMain(context.Background(), []string{"-check", "./app"}); got != 2 {
		t.Fatalf("expected exit 2 for an unreadable file, got %d", got)
	}
	// a missing dir (the argument itself is wrong) also exits 2.
	if got := runMain(context.Background(), []string{"-check", "./nope"}); got != 2 {
		t.Fatalf("expected exit 2 for a bad dir argument, got %d", got)
	}
	// write mode keeps exit 1 on real errors.
	if got := runMain(context.Background(), []string{"./app"}); got != 1 {
		t.Fatalf("expected exit 1 for an unreadable file in write mode, got %d", got)
	}
}

func TestExplainPrintsReasons(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// -explain prints each inferred directive with its inference path
	// in input vocabulary — the enum rule names the const members.
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, true, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "level.go explain: //go:generate stringer -type=Level") {
		t.Fatalf("-explain printed no line for the enum directive:\n%s", out)
	}
	if !strings.Contains(out, "enum:") {
		t.Fatalf("-explain gave no reason for the directive:\n%s", out)
	}
	// without the flag the reasons stay silent.
	buf.Reset()
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "explain:") {
		t.Fatalf("explain output leaked without -explain:\n%s", buf.String())
	}
}

func TestGeneratedFileSkipLogged(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// a generated-looking file is seen-and-skipped, never silent —
	// even when it earns no directives and has no managed block.
	content := "// Code generated by stringer -type=Level. DO NOT EDIT.\n\npackage app\n\nfunc (l Level) String() string { return \"\" }\n"
	target := filepath.Join(app, "level_string.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if _, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "level_string.go") || !strings.Contains(out, "skipping") {
		t.Fatalf("no skip line for the generated file:\n%s", out)
	}
	got, _ := os.ReadFile(target)
	if string(got) != content {
		t.Fatal("the generated file was modified")
	}
}

func TestPartialWriteSummary(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")
	// one file fails to write while the rest sync — the summary must
	// say "partial", not leave the failure looking like a clean pass.
	content := "package app\n\ntype Unwritable int\n\nconst (\n\tUnwA Unwritable = iota\n\tUnwB\n)\n"
	target := filepath.Join(app, "unw.go")
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod-444 is writable for root")
	}
	if err := os.Chmod(target, 0444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(target, 0644)
	var buf strings.Builder
	_, err := run(context.Background(), dir, scriptDir(t), app, false, false, false, &buf)
	if err == nil {
		t.Fatal("expected a write failure")
	}
	out := buf.String()
	if !strings.Contains(out, "1 file(s) failed to write") {
		t.Fatalf("no partial-write summary:\n%s", out)
	}
	// the rest of the package still synced — the failure is partial.
	if !strings.Contains(out, "file(s) updated") && !strings.Contains(out, "rewrote") && !strings.Contains(out, "inserted") {
		t.Fatalf("other files did not sync:\n%s", out)
	}
}

func TestNoBuildableReasonNamed(t *testing.T) {
	dir := setupModule(t)

	// a dir whose only .go file is build-constrained out must say so —
	// a permission problem and a constraint read identically without
	// the reason, and the package name belongs in the message.
	constr := filepath.Join(dir, "onlyconstr")
	if err := os.MkdirAll(constr, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(constr, "x.go"), []byte("//go:build ignore\n\npackage onlyconstr\n\ntype X int\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := run(context.Background(), dir, scriptDir(t), constr, false, false, false, io.Discard)
	if err == nil {
		t.Fatal("expected an error for a fully constrained package")
	}
	for _, want := range []string{"no buildable Go source files", "onlyconstr", "build constraints"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not say %q: %v", want, err)
		}
	}

	// a dir whose only file is unreadable must say permission, not
	// "no buildable" alone — the two cases are indistinguishable
	// without the reason.
	if os.Geteuid() == 0 {
		t.Skip("chmod-000 is readable for root")
	}
	unread := filepath.Join(dir, "onlyunread")
	if err := os.MkdirAll(unread, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(unread, "y.go")
	if err := os.WriteFile(target, []byte("package onlyunread\n\ntype Y int\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(target, 0644)
	_, err = run(context.Background(), dir, scriptDir(t), unread, false, false, false, io.Discard)
	if err == nil {
		t.Fatal("expected an error for an unreadable-only package")
	}
	if !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), "y.go") {
		t.Fatalf("error does not name the permission problem: %v", err)
	}
}

func TestDescribeFailureBlames(t *testing.T) {
	// the first line of a failure names who must fix it: the dir
	// argument, an input file at a position, a dependency, or the tool.
	trap := func(cause string) error {
		return &runtime.Trap{Reason: cause + "\nTraceback ...", Err: errors.New(cause)}
	}
	for _, c := range []struct {
		name string
		err  error
		dir  string
		want string
	}{
		{"dir arg not found", trap(`resolve dir "./nope": entry directory "./nope" not found: stat /x/nope: no such file`), "./nope", "fix the dir argument"},
		{"dir arg not buildable", trap(`resolve dir "./app": no buildable Go source files in package m/app (/x/app): all 1 .go file(s) excluded by build constraints`), "./app", "fix the dir argument"},
		{"permission inside resolve dir", trap(`resolve dir "./app": reading package dir /x/app: open /x/app: permission denied`), "./app", "permissions"},
		{"permission inside no buildable", trap(`resolve dir "./app": no buildable Go source files in package m/app (/x/app): open /x/app/y.go: permission denied`), "./app", "permissions"},
		{"permission inside dep resolve", trap(`resolve "m/app/internal/mood": reading package dir /x/mood: open /x/mood/m.go: permission denied`), "./app", "permissions"},
		{"permission inside import", trap(`import m/app: reading package dir /x/app: open /x/app/z.go: permission denied`), "./app", "permissions"},
		{"script dir missing is a tool problem", trap(`resolve dir "./script": entry directory "./script" not found`), "./app", "gen-sync bug"},
		{"input file parse", trap(`parse /x/app/level.go: /x/app/level.go:3:1: expected ';'`), "/x/app", "fix the input file"},
		{"dep file parse", trap(`parse /x/deps/mood/m.go: /x/deps/mood/m.go:1:1: expected`), "/x/app", "dependency"},
		{"import unresolvable", trap(`resolve "example.com/gone": resolving import "example.com/gone": import path "example.com/gone" could not be resolved`), "./app", "imports or the module"},
		{"script errors pass through", errors.New("gen-sync: /x/app/level.go: open /x/app/level.go: permission denied"), "./app", "gen-sync: /x/app/level.go"},
	} {
		got := describeFailure(c.err, c.dir, "./script")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: want blame %q in:\n%s", c.name, c.want, got)
		}
		if !strings.HasPrefix(got, "gen-sync:") && !strings.Contains(c.want, "gen-sync:") {
			t.Errorf("%s: output lost the gen-sync prefix:\n%s", c.name, got)
		}
	}
}
