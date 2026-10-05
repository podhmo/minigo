package main

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// setupModule copies the example into a temp module so generation never
// writes into the repo: go.mod (with its replace line repointed at the
// real checkout), the app fixture, and the stringer tool — directives
// reference ../tools/stringer relative to the scanned files.
func setupModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	// the committed go.mod's relative replace only resolves inside the
	// repo — the temp module needs the absolute path.
	patched := strings.ReplaceAll(string(data), "=> ../../", "=> "+root)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(patched), 0644); err != nil {
		t.Fatal(err)
	}
	copyTree(t, "app", filepath.Join(dir, "app"))
	copyTree(t, "tools", filepath.Join(dir, "tools"))
	return dir
}

// writeFailPkg adds a second scanned package beside app/: one directive
// the tool rejects (Count has no enum consts) and one it accepts.
func writeFailPkg(t *testing.T, dir string) {
	t.Helper()
	bad := `package fail

//minigo:generate ../tools/stringer -type=Count
type Count int
`
	good := `package fail

//minigo:generate ../tools/stringer -type=Color
type Color string

const (
	Red   Color = "red"
	Green Color = "green"
)
`
	pkg := filepath.Join(dir, "fail")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "aa.go"), []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "zz.go"), []byte(good), 0644); err != nil {
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

func runPkg(t *testing.T, dir string, re *regexp.Regexp, dry bool) int {
	t.Helper()
	ds, err := scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	return run(context.Background(), dir, ds, re, dry, false, io.Discard)
}

func TestGenerate(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")

	// GOFILE & co. are swapped in per directive and restored — the host
	// process keeps its own env.
	t.Setenv("GOFILE", "sentinel.go")

	if code := runPkg(t, app, nil, false); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	// two tools over one fixture: stringer on Status+Priority, enumvals
	// on Status (stacked directive lines) and Level.
	for _, name := range []string{"status_string", "priority_string", "status_values", "level_values"} {
		assertSameFile(t, filepath.Join(app, name+".go"), "testdata/"+name+".golden")
	}
	if v := os.Getenv("GOFILE"); v != "sentinel.go" {
		t.Fatalf("GOFILE leaked through the run: %q", v)
	}

	// rerun is idempotent: same exit code, same bytes.
	if code := runPkg(t, app, nil, false); code != 0 {
		t.Fatalf("rerun exited %d", code)
	}
	assertSameFile(t, filepath.Join(app, "status_string.go"), "testdata/status_string.golden")
}

func TestDryRun(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")

	if code := runPkg(t, app, nil, true); code != 0 {
		t.Fatalf("dry run exited %d", code)
	}
	ents, err := os.ReadDir(app)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range ents {
		if strings.HasSuffix(en.Name(), "_string.go") {
			t.Errorf("dry run wrote %s", en.Name())
		}
	}
}

func TestRunFilter(t *testing.T) {
	dir := setupModule(t)
	app := filepath.Join(dir, "app")

	re := regexp.MustCompile("Priority")
	if code := runPkg(t, app, re, false); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	assertSameFile(t, filepath.Join(app, "priority_string.go"), "testdata/priority_string.golden")
	for _, name := range []string{"status_string.go", "status_values.go", "level_values.go"} {
		if _, err := os.Stat(filepath.Join(app, name)); !os.IsNotExist(err) {
			t.Errorf("-run=Priority also wrote %s", name)
		}
	}

	// filtering on the tool name runs only that tool's directives.
	dir = setupModule(t)
	app = filepath.Join(dir, "app")
	re = regexp.MustCompile("enumvals")
	if code := runPkg(t, app, re, false); code != 0 {
		t.Fatalf("run exited %d", code)
	}
	assertSameFile(t, filepath.Join(app, "status_values.go"), "testdata/status_values.golden")
	assertSameFile(t, filepath.Join(app, "level_values.go"), "testdata/level_values.golden")
	if _, err := os.Stat(filepath.Join(app, "status_string.go")); !os.IsNotExist(err) {
		t.Error("-run=enumvals also ran stringer")
	}
}

func TestFailure(t *testing.T) {
	dir := setupModule(t)
	writeFailPkg(t, dir)
	pkg := filepath.Join(dir, "fail")

	// the failing directive is reported and the run exits nonzero, but
	// a later directive in the same package still runs.
	if code := runPkg(t, pkg, nil, false); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	assertSameFile(t, filepath.Join(pkg, "color_string.go"), "testdata/color_string.golden")
}

func TestSplitArgs(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"../tools/stringer -type=Status", []string{"../tools/stringer", "-type=Status"}},
		{`./tool -msg "hello world" -n=2`, []string{"./tool", "-msg", "hello world", "-n=2"}},
		{"./tool\t-x", []string{"./tool", "-x"}},
	} {
		got, err := splitArgs(tc.line, "f.go", 1)
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%q (-want +got):\n%s", tc.line, diff)
		}
	}
}
