package minigo_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/index"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
)

func newEngine(t *testing.T) *minigo.Engine {
	t.Helper()
	return minigo.NewEngine(".")
}

func run(t *testing.T, e *minigo.Engine, ref, fn string, args ...runtime.Value) runtime.Value {
	t.Helper()
	v, err := e.Run(context.Background(), ref, fn, args...)
	if err != nil {
		t.Fatalf("Run(%s, %s): %v", ref, fn, err)
	}
	return v
}

func TestEntryPoints(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		fn   string
		want any
	}{
		{"Answer", int64(67)}, // fib(10)=55 + Global=10 + B=2
		{"SumRange", int64(0 + 1 + 1 + 2 + 2 + 3 + 3 + 4)},
		{"Methods", int64(7)},
		{"Closure", int64(3)},
		{"Multi", int64(43)},
		{"Mapy", int64(5)},
		{"Consts", int64(6)},
		{"Strings", "hello!!!"},
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/t1", c.fn)
		if got != c.want {
			t.Errorf("%s: got %v (%T), want %v (%T)", c.fn, got, got, c.want, c.want)
		}
	}
}

func TestSwitchy(t *testing.T) {
	e := newEngine(t)
	for in, want := range map[int64]int64{1: 10, 2: 10, 3: 30, 99: -1} {
		got, err := e.Run(context.Background(), "./testdata/t1", "Switchy", in)
		if err != nil {
			t.Fatalf("Switchy(%d): %v", in, err)
		}
		if got != want {
			t.Errorf("Switchy(%d) = %v, want %d", in, got, want)
		}
	}
}

func TestLazyImport(t *testing.T) {
	e := newEngine(t)
	// OK never references lazyboom -> its panicking init must not run
	if got := run(t, e, "./testdata/lazyuser", "OK"); got != int64(1) {
		t.Fatalf("OK: got %v", got)
	}
	// Bad touches lazyboom.Get -> package init runs -> panic surfaces
	_, err := e.Run(context.Background(), "./testdata/lazyuser", "Bad")
	if err == nil || !strings.Contains(err.Error(), "BOOM") {
		t.Fatalf("Bad: expected BOOM panic, got %v", err)
	}
}

func TestBlankImportInitializes(t *testing.T) {
	e := newEngine(t)
	_, err := e.Run(context.Background(), "./testdata/blankimport", "OK")
	if err == nil || !strings.Contains(err.Error(), "BOOM") {
		t.Fatalf("blank import must initialize before entry: %v", err)
	}
}

func TestDotImports(t *testing.T) {
	e := newEngine(t)
	for _, tc := range []struct {
		name string
		want runtime.Value
	}{
		{name: "Greeting", want: "hi x"},
		{name: "Number", want: int64(11)},
	} {
		got := run(t, e, "./testdata/dotimports", tc.name)
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", tc.name, diff)
		}
	}
	_, err := e.Run(context.Background(), "./testdata/dotimports", "TouchBoom")
	if err == nil || !strings.Contains(err.Error(), "BOOM") {
		t.Fatalf("dot-imported member must initialize its package: %v", err)
	}
}

func TestDepOrderAndFixes(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		fn   string
		want any
	}{
		{"Dep", int64(2)},          // var B = A+1 before var A — dep order
		{"ImportInit", "hi x"},     // import inside a package-level init expr
		{"ForContinue", int64(25)}, // 1+3+5+7+9
		{"RangeOne", int64(12)},    // single-var range yields indexes 0,1,2
		{"Redefine", int64(56)},    // x,y := keeps existing x binding
		{"StructCopy", int64(1)},   // b := a copies struct value
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/deporder", c.fn)
		if got != c.want {
			t.Errorf("%s: got %v (%T), want %v (%T)", c.fn, got, got, c.want, c.want)
		}
	}
}

func TestTrapOnCall(t *testing.T) {
	e := newEngine(t)
	// invariant: unsupported constructs compile fine, trap only when called
	if got := run(t, e, "./testdata/traponcall", "Good"); got != int64(7) {
		t.Fatalf("Good: got %v", got)
	}
	_, err := e.Run(context.Background(), "./testdata/traponcall", "Bad")
	if err == nil || !strings.Contains(err.Error(), "3-index") {
		t.Fatalf("Bad: expected 3-index-slice trap, got %v", err)
	}
	_, err = e.Run(context.Background(), "./testdata/traponcall", "Channy")
	if err == nil || !strings.Contains(err.Error(), "label not defined") {
		t.Fatalf("Channy: expected undefined-label trap, got %v", err)
	}
	// fallthrough + type assert work now: both old traps became real code
	if got := run(t, e, "./testdata/traponcall", "FallthroughAndAssert"); got != int64(16) {
		t.Fatalf("FallthroughAndAssert: got %v", got)
	}
}

func TestDeferRecover(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		fn   string
		want runtime.Value
	}{
		{"DeferOrder", int64(321)},        // defers run LIFO
		{"DeferArgCaptured", int64(1)},    // args evaluated at defer time
		{"NamedResultDefer", int64(15)},   // defer mutates named result
		{"RecoverValue", "boom"},          // defer+recover captures panic
		{"RecoverOutsideDefer", int64(1)}, // recover outside defer is nil
		{"StillPanic", runtime.NIL},       // recovered panic returns normally
		{"DeferBuiltinClose", int64(2)},   // deferred builtin runs at teardown
		{"DeferBuiltinRecover", int64(3)}, // defer recover() catches the panic
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/deferchan", c.fn)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
		}
	}
	// a panic raised inside a defer propagates
	_, err := e.Run(context.Background(), "./testdata/deferchan", "ReraiseReplace")
	if err == nil || !strings.Contains(err.Error(), "second") {
		t.Fatalf("ReraiseReplace: expected 'second' panic, got %v", err)
	}
}

func TestChanSelect(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		fn   string
		want runtime.Value
	}{
		{"ChanQueue", int64(321)},     // FIFO send/receive
		{"ChanCommaOk", int64(1)},     // comma-ok on non-empty
		{"ChanClosedRecv", int64(42)}, // closed empty recv -> nil,false
		{"ChanRange", int64(60)},      // range drains the queue
		{"GoSync", int64(12)},         // go f() runs synchronously
		{"GoChanRoundtrip", int64(42)},
		{"SelectRecv", int64(5)},
		{"SelectDefault", int64(9)},
		{"SelectCommaOk", int64(3)},
		{"SelectSend", int64(11)},
		{"SelectConsumeBare", int64(2)},   // bare case <-ch consumes
		{"SelectEvalOrder", int64(11)},    // all operands eval on entry
		{"SelectSendEvalOrder", int64(7)}, // send chan+value eval too
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/deferchan", c.fn)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
		}
	}
}

func TestInitOrderThroughFunc(t *testing.T) {
	e := newEngine(t)
	// var x = f() where f reads var y declared later: y must initialize first
	if got := run(t, e, "./testdata/initorder", "Answer"); got != int64(14) {
		t.Fatalf("Answer: got %v, want 14", got)
	}
}

func TestStdlibIntrinsics(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		fn   string
		want runtime.Value
	}{
		{"Sprintf", "hi 7"},        // real fmt.Sprintf without GOROOT parse
		{"StrconvAtoi", int64(42)}, // (val, err) tuple
		{"StringsJoin", "a,b,c"},
		{"ErrorsNew", "oops"},
		{"SortIntsInPlace", int64(123)}, // sort mutates the slice
		{"SlicesSortInPlace", "abc"},    // slices.Sort mutates too
		{"SortSearch", int64(6)},
		{"SortStableByLen", "adbbcc"},
		{"SortStableFuncByLen", "adbbcc"},
		{"BinarySearchHit", int64(2)},
		{"BinarySearchMiss", int64(2)},
		{"BinarySearchFunc", int64(3)},
		{"BinarySearchNamed", int64(1)}, // named-string elements
		{"RuntimeGOOS", true},
		{"RuntimeGoroutines", int64(1)}, // single-threaded approximation
		{"RuntimeGOMAXPROCS", int64(1)}, // read-only: setter arg is ignored
		{"UnsafeSizeofInt", int64(8)},
		{"UnsafeSizeofSlice", int64(24)},
		{"UnsafeAlignofEmpty", int64(1)},
		{"StringsSplitN", "b:c"},
		{"StringsTrim", "x"},
		{"StringsLastIndex", int64(3)},
		{"StringsTitle", "Hello World"},
		{"StrconvFormatUint", "ff"},
		{"StrconvIsPrint", true},
		{"BytesBuffer", "xyz"},
		{"BytesFields", "a"},
		{"Utf8Count", int64(5)},
		{"Utf8Encode", "☺"},
		{"UnicodeDigit", true},
		{"MathRound", true},
		{"RegexpReplace", "bXc"},
		{"RegexpMatch", true},
		{"Base64Enc", "aGk="},
		{"Base64Dec", "hi"},
		{"HexEnc", "6869"},
		{"UrlEsc", "a+b%26c"},
		{"UrlJoin", "https://x.example/base/a/b.txt"},
		{"HtmlEsc", "&lt;b&gt;&amp;"},
		{"PathJoin", "a/b/c.txt"},
		{"PathSplit", "/a/b/|c.txt"},
		{"JsonMarshal", `{"x":1,"ys":["a","b"]}`},
		{"JsonMarshalStruct", `{"X":1,"Y":"a"}`},
		{"JsonUnmarshal", "ok"},
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/intrins", c.fn)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
		}
	}
}

func TestConstAssignTraps(t *testing.T) {
	e := newEngine(t)
	_, err := e.Run(context.Background(), "./testdata/constreassign", "AssignConst")
	if err == nil {
		t.Fatal("expected a cannot-assign-to-const trap")
	}
	if !strings.Contains(err.Error(), "cannot assign") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvalExpr(t *testing.T) {
	e := newEngine(t)
	pkg, err := e.Package(context.Background(), "./testdata/t1")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	expr, err := parser.ParseExprFrom(fset, "eval.go", "Global + 2", 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.EvalExpr(context.Background(), pkg, nil, expr)
	if err != nil {
		t.Fatalf("EvalExpr: %v", err)
	}
	if diff := cmp.Diff(int64(12), got); diff != "" {
		t.Errorf("EvalExpr mismatch (-want +got):\n%s", diff)
	}
}

func TestAllowedRoots(t *testing.T) {
	// Roots pinned to ./testdata: t1 lives inside and must run, /tmp refuses.
	td, err := filepath.Abs("./testdata")
	if err != nil {
		t.Fatal(err)
	}
	e := minigo.NewEngine("..", minigo.WithAllowedRoots(td))
	if _, err := e.Run(context.Background(), "./testdata/t1", "Answer"); err != nil {
		t.Fatalf("inside allowed root should run: %v", err)
	}
	outside := filepath.Dir(os.TempDir())
	if strings.HasPrefix(td, outside+string(filepath.Separator)) || td == outside {
		outside = "/"
	}
	if _, err := e.Run(context.Background(), outside, "main"); err == nil ||
		!strings.Contains(err.Error(), "outside the allowed roots") {
		t.Fatalf("expected outside-root rejection, got %v", err)
	}
}

func TestLazyInitMode(t *testing.T) {
	// LazyInit answers function/type queries without running initializers:
	// lazyboom's panicking init must NOT run when we only ask for its Func.
	e := minigo.NewEngine("..", minigo.WithInitMode(minigo.LazyInit))
	pkg, err := e.Package(context.Background(), "./testdata/lazyboom")
	if err != nil {
		t.Fatal(err)
	}
	stub := func(p *runtime.Package, d *index.Decl) (runtime.Value, error) {
		return runtime.NIL, nil
	}
	if _, err := pkg.Member("Get", stub); err != nil {
		t.Fatalf("Member(Get): %v", err)
	}
	if pkg.State == runtime.Ready {
		t.Fatal("LazyInit Member must not initialize the package")
	}
	// GoCompatibleInit (default) surfaces the panic at member touch.
	e2 := newEngine(t)
	pkg2, err := e2.Package(context.Background(), "./testdata/lazyboom")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pkg2.Member("Get", stub); err == nil || !strings.Contains(err.Error(), "BOOM") {
		t.Fatalf("eager mode should surface init panic, got %v", err)
	}
}

func TestInitFailureSurfaces(t *testing.T) {
	// An initializer that panics after registering some globals must not
	// leave partial state answerable: Member returns the init error.
	e := newEngine(t)
	pkg, err := e.Package(context.Background(), "./testdata/initfail")
	if err != nil {
		t.Fatal(err)
	}
	stub := func(p *runtime.Package, d *index.Decl) (runtime.Value, error) {
		return runtime.NIL, nil
	}
	if _, err := pkg.Member("Good", stub); err == nil ||
		!strings.Contains(err.Error(), "init went wrong") {
		t.Fatalf("partial init state must surface the failure, got %v", err)
	}
	if _, err := e.Run(context.Background(), "./testdata/initfail", "Use"); err == nil ||
		!strings.Contains(err.Error(), "init went wrong") {
		t.Fatalf("Run on a failed package must surface the init error, got %v", err)
	}
}

func TestOsHostSurface(t *testing.T) {
	// Restricted engines (AllowedRoots set) do not get os.Getenv/os.Args.
	td, err := filepath.Abs("./testdata")
	if err != nil {
		t.Fatal(err)
	}
	e := minigo.NewEngine("..", minigo.WithAllowedRoots(td))
	if _, err := e.Run(context.Background(), "./testdata/hostenv", "Read"); err == nil {
		t.Fatal("os.Getenv must be unbound under AllowedRoots")
	}
	// os.Exit never terminates the host, in any mode.
	if _, err := newEngine(t).Run(context.Background(), "./testdata/hostenv", "Exit"); err == nil ||
		!strings.Contains(err.Error(), "cannot terminate the host") {
		t.Fatalf("os.Exit must trap, got %v", err)
	}
}

func TestFSIntrinsics(t *testing.T) {
	e := newEngine(t)
	dir := t.TempDir()
	for _, c := range []struct {
		fn   string
		args []runtime.Value
		want runtime.Value
	}{
		{"WriteThenRead", []runtime.Value{dir}, "hello"},
		{"StatSize", []runtime.Value{dir}, int64(3)},
		{"IsNotExistHit", []runtime.Value{dir}, true},
		{"ListDir", []runtime.Value{dir}, "inner,x.txt,y.txt|dirs=1"},
		{"GlobMatch", []runtime.Value{dir}, filepath.Join(dir, "a.txt")},
		{"WalkCollect", []runtime.Value{dir}, int64(1)},
		{"FileWrite", []runtime.Value{dir}, "rw"},
		{"MatchHit", nil, true},
	} {
		// each case gets its own directory so writes do not leak between runs
		args := make([]runtime.Value, len(c.args))
		for i, a := range c.args {
			if s, ok := a.(string); ok {
				a = filepath.Join(s, c.fn)
				if err := os.MkdirAll(a.(string), 0755); err != nil {
					t.Fatal(err)
				}
			}
			args[i] = a
		}
		want := c.want
		if ws, ok := want.(string); ok && strings.HasPrefix(ws, dir+string(filepath.Separator)) {
			want = filepath.Join(dir, c.fn, filepath.Base(ws))
		}
		got, err := e.Run(context.Background(), "./testdata/fsops", c.fn, args...)
		if err != nil {
			t.Fatalf("%s: %v", c.fn, err)
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
		}
	}
}

func TestVirtualCwd(t *testing.T) {
	e := newEngine(t)
	dir := t.TempDir()
	// script chdirs into dir, writes relatively, then restores: the file
	// lands inside dir and the engine cwd comes back to the repo root.
	got := run(t, e, "./testdata/fsops", "ChdirRoundtrip", dir)
	want := filepath.Join(dir, "rel.txt")
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("ChdirRoundtrip mismatch (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("relative write did not land in the virtual cwd: %v", err)
	}
	cwd, _ := os.Getwd()
	if strings.HasPrefix(cwd, dir+string(filepath.Separator)) {
		t.Fatal("script chdir leaked into the host process cwd")
	}
}

func TestExecIntrinsics(t *testing.T) {
	e := newEngine(t)
	dir := t.TempDir()
	if got := run(t, e, "./testdata/fsops", "ExecEcho"); got != "hi" {
		t.Fatalf("ExecEcho: got %v", got)
	}
	// exec.Cmd.Dir defaults to the engine's virtual cwd
	wantCwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, e, "./testdata/fsops", "CmdDirField"); got != wantCwd {
		t.Fatalf("CmdDirField: got %v, want %v", got, wantCwd)
	}
	// field set on a host struct: cmd.Dir = dir, then pwd reports it
	got := run(t, e, "./testdata/fsops", "ExecDirField", dir)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != resolved {
		t.Fatalf("ExecDirField: got %v, want %v", got, resolved)
	}
	// exec.LookPath with a separator-bearing relative name anchors at the
	// engine's virtual cwd, not the host process's cwd
	tool := filepath.Join(dir, "tool.bin")
	if err := os.WriteFile(tool, []byte("#!"), 0755); err != nil {
		t.Fatal(err)
	}
	if got := run(t, e, "./testdata/fsops", "LookPathLocal", dir); got != "./tool.bin" {
		t.Fatalf("LookPathLocal: got %v", got)
	}
}

func TestFSRestricted(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	script := `package main

import (
	"os"
	"path/filepath"
	"strings"
)

func WriteInside(name string) string {
	if err := os.WriteFile(name, "x", 0644); err != nil {
		return "v:" + err.Error()
	}
	return filepath.Base(name)
}

func ReadOutside(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "v:" + err.Error()
	}
	return string(data)
}

func WriteOutside(path string) string {
	if err := os.WriteFile(path, "x", 0644); err != nil {
		return "v:" + err.Error()
	}
	return "wrote"
}

func GlobStar() string {
	m, err := filepath.Glob("*/e.txt")
	if err != nil {
		return "caught"
	}
	return strings.Join(m, ",")
}
`
	fname := filepath.Join(root, "taskfile.go")
	if err := os.WriteFile(fname, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	e := minigo.NewEngine(root, minigo.WithAllowedRoots(root))
	// inside-root relative write anchors at the virtual cwd and succeeds
	if got, err := e.RunFile(context.Background(), fname, "WriteInside", "inside.txt"); err != nil || got != "inside.txt" {
		t.Fatalf("WriteInside: got %v, err %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "inside.txt")); err != nil {
		t.Fatalf("inside-root write missing: %v", err)
	}
	// escape attempts fail the call itself — per-call path checks
	out := filepath.Join(outside, "x.txt")
	if err := os.WriteFile(out, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		fn   string
		args []runtime.Value
	}{
		{"ReadOutside", []runtime.Value{out}},
		{"WriteOutside", []runtime.Value{filepath.Join(outside, "y.txt")}},
	} {
		if _, err := e.RunFile(context.Background(), fname, tc.fn, tc.args...); err == nil ||
			!strings.Contains(err.Error(), "outside the allowed roots") {
			t.Fatalf("%s: expected outside-root rejection, got %v", tc.fn, err)
		}
	}
	// a glob whose matches escape through an in-root symlink must fail:
	// the pattern itself sits inside the roots, but expansion follows the
	// link, so each match is re-checked per call
	if err := os.WriteFile(filepath.Join(outside, "e.txt"), []byte("e"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	// the escape surfaces as the call's error value — script-catchable —
	// never as out-of-root match names
	if got, err := e.RunFile(context.Background(), fname, "GlobStar"); err != nil || got != "caught" {
		t.Fatalf("GlobStar: expected script-caught rejection, got %v, %v", got, err)
	}
	os.Remove(filepath.Join(root, "link"))

	// os/exec must not be bound at all under AllowedRoots
	execScript := `package main

import (
	"os/exec"
)

func Probe() string {
	return exec.Command("echo").Dir
}
`
	execFile := filepath.Join(root, "execfile.go")
	if err := os.WriteFile(execFile, []byte(execScript), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RunFile(context.Background(), execFile, "Probe"); err == nil {
		t.Fatal("os/exec must be unbound under AllowedRoots")
	}
}

func TestHostStubPackage(t *testing.T) {
	e := newEngine(t)
	// the in-repo stub path resolves to intrinsics, never the stub bodies
	if got := run(t, e, "./testdata/hostpkg", "EnvPath"); got == "" {
		t.Fatal("host.Getenv(PATH) should return the host PATH")
	}
	if got := run(t, e, "./testdata/hostpkg", "Argc"); got.(int64) < 1 {
		t.Fatalf("host.Args should be non-empty, got %v", got)
	}
	if got := run(t, e, "./testdata/hostpkg", "Host"); got == "" || got == "err" {
		t.Fatalf("host.Hostname: got %v", got)
	}
	if got := run(t, e, "./testdata/hostpkg", "Cwd"); got != "ok" {
		t.Fatalf("host.Getwd: got %v", got)
	}
	// the canonical minigo.dev/host path binds the same intrinsics
	if got := run(t, e, "./testdata/hostdev", "EnvPath"); got == "" {
		t.Fatal("minigo.dev/host.Getenv(PATH) should return the host PATH")
	}
	// host.Exit never terminates the host, like os.Exit
	_, err := e.Run(context.Background(), "./testdata/hostpkg", "Exit")
	if err == nil || !strings.Contains(err.Error(), "cannot terminate the host") {
		t.Fatalf("host.Exit must trap, got %v", err)
	}
	// restricted engines drop the environment surface
	td, err := filepath.Abs("./testdata")
	if err != nil {
		t.Fatal(err)
	}
	re := minigo.NewEngine("..", minigo.WithAllowedRoots(td))
	if _, err := re.Run(context.Background(), "./testdata/hostpkg", "EnvPath"); err == nil {
		t.Fatal("host.Getenv must be unbound under AllowedRoots")
	}
}

func TestFeatures(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		fn   string
		want runtime.Value
	}{
		// spread calls
		{"Spread", int64(36)},
		{"SpreadTail", int64(8)},
		// compound assign & ++/-- on fields, indices, derefs
		{"CompoundOps", int64(172)},
		{"PtrOps", int64(12)},
		// labels, break/continue L, goto
		{"LabeledBreak", int64(3)},
		{"LabeledContinue", int64(16)},
		{"GotoSkip", int64(5)},
		{"LabeledSwitch", int64(3)},
		{"SelectLabel", int64(2)},
		// interfaces: dispatch, embedding, satisfaction
		{"InterfaceDispatch", int64(16)},
		{"EmbeddedMethod", int64(25)},
		{"IfaceHolds", int64(1)},
		// assertions + type switches
		{"AssertFail", false},
		{"TypeSwitch", int64(120)},
		{"TypeSwitchBind", "hey!"},
		{"AssertPanic", int64(99)},
		{"NilAssert", int64(1)},
		// nil-slice semantics + comma-ok zero + elided literal types
		{"NilRange", int64(0)},
		{"AppendNil", int64(4)},
		{"CommaOkZero", int64(5)},
		{"ElidedLits", int64(19)},
		{"NamedElided", int64(5)},
		// generics
		{"GenericFns", int64(42)},
		{"GenericConvert", int64(42)},
		{"GenericType", int64(42)},
		// range-over-func (iter.Seq/Seq2)
		{"SeqIter", int64(60)},
		{"SeqIter2", int64(5)},
		{"SeqBreak", int64(10)},
		{"SeqContinue", int64(40)},
		{"SeqReturn", "early exit"},
		{"SeqGenerator", int64(10)},
		{"SeqNoVars", int64(3)},
		{"SeqNested", int64(90)},
		{"SeqBodyPanic", int64(42)},
		{"SeqYieldAfterFalse", int64(7)},
		{"SeqDefer", int64(9)},
		{"SeqNamed", int64(6)},
		{"SeqLabelBreak", int64(1)},
		{"SeqGotoLoop", int64(9)},
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/features", c.fn)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
		}
	}
	// comma-ok assert returns a tuple
	got := run(t, e, "./testdata/features", "AssertOK")
	tp, ok := got.(*runtime.Tuple)
	if !ok || len(tp.Elems) != 2 || tp.Elems[0] != int64(2) || tp.Elems[1] != true {
		t.Fatalf("AssertOK: got %v", got)
	}
}

func TestSpecialForms(t *testing.T) {
	e := minigo.NewEngine(".")
	e.Bind("example.com/dsl", map[string]runtime.Value{})

	twice := func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
		v, err := ctx.Eval(call.Call.Args[0])
		if err != nil {
			return nil, err
		}
		n, ok := v.(int64)
		if !ok {
			return nil, ctx.Errorf(call.Call, "Twice arg is %T, want int", v)
		}
		return n * 2, nil
	}
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/dsl", Name: "Twice"}, twice)
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/dsl", Name: "Show"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			// the quoted arg renders as source, never evaluated
			return ctx.Format(call.Call.Args[0]), nil
		})
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/dsl", Name: "Skipped"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			return int64(42), nil // arg never evaluated -> no panic
		})
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/dsl", Name: "SymOf"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			id, err := ctx.ResolveSymbol(call.Call.Args[0])
			if err != nil {
				return nil, err
			}
			return id.PackagePath + "::" + id.Name, nil
		})
	// Resolve maps a symbol expr to its value lazily — call it to prove
	// the materialized function arrived intact.
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/dsl", Name: "ResOf"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			v, err := ctx.Resolve(call.Call.Args[0])
			if err != nil {
				return nil, err
			}
			return ctx.Call(v, []runtime.Value{"go"})
		})
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/dsl", Name: "ResOf0"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			v, err := ctx.Resolve(call.Call.Args[0])
			if err != nil {
				return nil, err
			}
			return ctx.Call(v, nil)
		})
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/dsl", Name: "ResIdent"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			return ctx.Resolve(call.Call.Args[0])
		})
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/dsl", Name: "TypeName"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			td, err := ctx.ResolveType(call.Call.Args[0])
			if err != nil {
				return nil, err
			}
			return td.Name, nil
		})
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/dsl", Name: "TypeKind"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			td, err := ctx.ResolveType(call.Call.Args[0])
			if err != nil {
				return nil, err
			}
			return int64(td.Kind), nil
		})

	cases := []struct {
		fn   string
		want runtime.Value
	}{
		{"TwiceIt", int64(44)}, // Eval sees caller's local x=21
		{"Quoted", "y + 1"},    // quoted, unevaluated source
		{"Lazy", int64(42)},    // handler never Evals -> boom() never runs
		// ResolveSymbol resolves through the import table only: lazyboom's
		// panicking init must not run, proving index-level laziness.
		{"SymPkg", "github.com/podhmo/minigo/testdata/lazyboom::Get"},
		{"SymGreet", "github.com/podhmo/minigo/testdata/greet::Hello"},
		// Resolve maps the symbol to its value; ResOf calls it.
		{"ResGreet", "hi go"},
		{"ResSelf", int64(44)},
		{"ResLocal", int64(33)},
		// ResolveType answers type queries on quoted type expressions.
		{"TypeNamed", "Point"},
		{"TypeBuiltin", "int"},
		{"TypeSlice", int64(runtime.KindSlice)},
		{"TypePtr", int64(runtime.KindPointer)},
		{"TypeParamInt", "int"},
		{"TypeParamInfer", "int"},
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/special", c.fn)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
		}
	}
	// a bare ident resolves to a member of the caller's own package
	got := run(t, e, "./testdata/special", "SymSelf")
	s, ok := got.(string)
	if !ok || !strings.HasSuffix(s, "::TwiceIt") {
		t.Fatalf("SymSelf: got %v", got)
	}
	// a local variable is not a package symbol — the handler's error
	// surfaces through Run
	_, err := e.Run(context.Background(), "./testdata/special", "SymLocal")
	if err == nil || !strings.Contains(err.Error(), "local variable") {
		t.Fatalf("SymLocal: expected local-variable error, got %v", err)
	}
	// a local variable shadowing an import alias is not a package symbol
	_, err = e.Run(context.Background(), "./testdata/special", "SymShadow")
	if err == nil || !strings.Contains(err.Error(), "local variable") {
		t.Fatalf("SymShadow: expected local-variable error, got %v", err)
	}
}

func TestHostPolicy(t *testing.T) {
	// deny the whole os package: bound but empty — script sees "undefined"
	e := minigo.NewEngine("..", minigo.WithHostPolicy(func(path, sym string) bool {
		return path != "os"
	}))
	if got := run(t, e, "./testdata/hostpolicy", "PolicyOK"); got != "OK" {
		t.Fatalf("PolicyOK: got %v", got)
	}
	_, err := e.Run(context.Background(), "./testdata/hostpolicy", "PolicyDenied")
	if err == nil || !strings.Contains(err.Error(), "Getenv") {
		t.Fatalf("os.Getenv must be denied by policy, got %v", err)
	}
}

func TestDeclTypes(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		fn   string
		want runtime.Value
	}{
		{"ZeroStruct", int64(0)},
		{"PkgLevelTypedZero", int64(1)},
		{"TypedNils", int64(304)},
		{"NilOps", int64(1)},
		{"PtrNil", int64(1)},
		{"IfaceNilBoxes", int64(1)},
		{"FuncNil", int64(1)},
		{"LocalType", int64(5)},
		{"LocalTypeAssert", int64(3)},
		{"PtrElems", int64(16)},
		{"NamedPtrVar", int64(1)},
		{"MapKeyIdent", int64(10)},
		{"ArrKeyIdent", int64(16)},
		{"NamedReturnNil", int64(1)},
		{"ReturnNilTyped", int64(1)},
		{"ParamIfaceBox", int64(1)},
		{"InferCalls", int64(42)},
		{"GenericZero", int64(1)},
		{"ConstraintOK", int64(42)},
		{"SliceElemBox", int64(1)},
		{"SliceAlias", int64(1)},
		{"VarargZero", int64(8)}, // 0+1 from empty rest; 3+4 from 3 elems
		{"MapMissZero", int64(1)},
		{"NamedZero", int64(1)},
		{"MultiReturnBox", int64(1)},
		{"NamedIdent", 36.5*9/5 + 32}, // Celsius keeps its declared type
		{"NamedOps", "a!"},
		{"NamedStore", int64(42)},
		{"NamedAssert", int64(4)},
		{"MapBindZero", int64(1)},
		{"FieldHoleZero", int64(1)},
		{"AssignOK", int64(42)},
		{"IfaceNilOK", int64(1)},
		{"NamedPtrIface", int64(6)},
		{"NamedUnary", int64(-4)},
		{"AliasBindOK", int64(1)},
		{"StructAliasOK", int64(3)},
		{"AnonStructOK", int64(4)},
		{"PtrElemOK", int64(7)},
		{"BoxStoreOK", int64(5)},
		{"MapAliasOK", int64(9)},
		{"SliceElemTyped", int64(7)},
		{"ChanElemTyped", int64(3)},
		{"ConstTyped", int64(6)},
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/decltypes", c.fn)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
		}
	}
}

func TestDeclAssignability(t *testing.T) {
	e := newEngine(t)
	// `var x T = v` and `x = v` enforce assignability at bind/store time —
	// named types, basic families, interface method sets and typed-nil
	// shapes all check like the Go type checker, but only when the
	// statement actually runs (the compiler stays total).
	bads := []struct {
		fn  string
		sub string
	}{
		{"AssignBadInt", "cannot use"},
		{"AssignBadNamed", "cannot use"},
		{"AssignMix", "mismatched types"},
		{"IfaceBad", "cannot use"},
		{"NilBadShape", "cannot use"},
		{"MapBindTrap", "cannot use"},
		{"NoInheritBad", "no field or method"},
		{"IfaceNilBad", "cannot use"},
		{"ChainHoleBad", "cannot use"},
		{"StructNamedBad", "cannot use"},
		{"PtrElemBad", "cannot use"},
		{"BoxStoreBad", "cannot use"},
		{"MapRebindBad", "cannot use"},
		{"SliceRebindBad", "cannot use"},
	}
	for _, c := range bads {
		if _, err := e.Run(context.Background(), "./testdata/decltypes", c.fn); err == nil ||
			!strings.Contains(err.Error(), c.sub) {
			t.Fatalf("%s: expected %q trap, got %v", c.fn, c.sub, err)
		}
	}
}

func TestConstraintRejects(t *testing.T) {
	e := newEngine(t)
	// TakesNum[bool]: bool does not satisfy ~int|~string — the type
	// instantiation itself traps, like the Go type checker.
	_, err := e.Run(context.Background(), "./testdata/decltypes", "ConstraintBad")
	if err == nil || !strings.Contains(err.Error(), "constraint") {
		t.Fatalf("ConstraintBad: expected constraint trap, got %v", err)
	}
}

func TestGotoViolations(t *testing.T) {
	e := newEngine(t)
	if got := run(t, e, "./testdata/gotoviol", "Good"); got != int64(7) {
		t.Fatalf("Good: got %v", got)
	}
	if got := run(t, e, "./testdata/gotoviol", "Fine"); got != int64(3) {
		t.Fatalf("Fine: got %v", got)
	}
	_, err := e.Run(context.Background(), "./testdata/gotoviol", "IntoBlock")
	if err == nil || !strings.Contains(err.Error(), "jumps into a block") {
		t.Fatalf("IntoBlock: expected into-block trap, got %v", err)
	}
	_, err = e.Run(context.Background(), "./testdata/gotoviol", "OverDecl")
	if err == nil || !strings.Contains(err.Error(), "jumps over declaration") {
		t.Fatalf("OverDecl: expected over-decl trap, got %v", err)
	}
	_, err = e.Run(context.Background(), "./testdata/gotoviol", "Shadow")
	if err == nil || !strings.Contains(err.Error(), "jumps over declaration of x") {
		t.Fatalf("Shadow: expected over-decl trap, got %v", err)
	}
}

// TestSessionInheritsBinds: user-bound host packages must resolve in a
// NewSession the same as on the parent engine.
func TestSessionInheritsBinds(t *testing.T) {
	e := newEngine(t)
	e.Bind("myhost/lib", map[string]runtime.Value{
		"Magic": &runtime.BuiltinFunc{
			Name: "Magic",
			Fn: func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
				return int64(42), nil
			},
		},
	})
	got := run(t, e.NewSession(), "./testdata/sessbind", "Main")
	if diff := cmp.Diff(int64(42), got); diff != "" {
		t.Fatalf("Main mismatch (-want +got):\n%s", diff)
	}
}

func TestImportByDeclaredName(t *testing.T) {
	e := newEngine(t)
	// the package clause wins over the import path's last element
	got := run(t, e, "./testdata/pkgname", "Use")
	if diff := cmp.Diff(int64(7), got); diff != "" {
		t.Fatalf("Use mismatch (-want +got):\n%s", diff)
	}
}

func TestWithOutput(t *testing.T) {
	var buf strings.Builder
	e := minigo.NewEngine("..", minigo.WithOutput(&buf))
	run(t, e, "./testdata/intrins", "Prints")
	if got := buf.String(); got != "hello 42\n" {
		t.Fatalf("output: got %q", got)
	}
}

func TestResultAs(t *testing.T) {
	e := newEngine(t)
	r, err := e.RunResult(context.Background(), "./testdata/decltypes", "TypedNils")
	if err != nil {
		t.Fatalf("RunResult: %v", err)
	}
	var n int64
	if err := r.As(&n); err != nil {
		t.Fatalf("As: %v", err)
	}
	if n != 304 {
		t.Fatalf("As: got %d", n)
	}
}

// ---- file-level entries + the convert-define shape (plan §12) ----

// spyResolver counts Locate/LocateDir calls to prove laziness claims: a
// package the engine materializes always shows up as a resolver hit.
type spyResolver struct {
	inner   resolve.Resolver
	located []string
	dirs    []string
}

func (s *spyResolver) Locate(ctx context.Context, fromDir, importPath string) (*resolve.PackageMeta, error) {
	s.located = append(s.located, importPath)
	return s.inner.Locate(ctx, fromDir, importPath)
}

func (s *spyResolver) LocateDir(ctx context.Context, dir string) (*resolve.PackageMeta, error) {
	s.dirs = append(s.dirs, dir)
	return s.inner.LocateDir(ctx, dir)
}

func TestLoadFile(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t)

	// defs.go carries //go:build codegen: directory-mode loading filters it
	// out; LoadFile takes the named file regardless.
	pkg, err := e.LoadFile(ctx, "./testdata/dslfile/defs.go")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(pkg.Files) != 1 {
		t.Fatalf("single-file package: got %d files", len(pkg.Files))
	}
	if got := pkg.Files[0].AST.Name.Name; got != "main" {
		t.Errorf("package name: got %q, want main", got)
	}
	// the untagged sibling other.go is not part of the package
	if _, err := e.Call(ctx, pkg, "OtherOnly"); err == nil {
		t.Errorf("OtherOnly must be absent: only defs.go makes the package")
	}
	pkg2, err := e.LoadFile(ctx, "./testdata/dslfile/defs.go")
	if err != nil || pkg2 != pkg {
		t.Errorf("LoadFile must cache by path: %v", err)
	}
}

// TestSpecialFormsConvertDefineStyle is the §12 acceptance test: a
// convert-define-shaped DSL file runs through SPECIAL_CALL with every
// import quoted — none of example.com/{define,convutil,source,destination}
// exists on disk, so any materialization attempt fails the run.
func TestSpecialFormsConvertDefineStyle(t *testing.T) {
	ctx := context.Background()

	res, err := resolve.NewGoScanResolver("..", resolve.BuildConfig{})
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	spy := &spyResolver{inner: res}
	e := minigo.NewEngine(".").WithResolver(spy)

	importPathOf := func(ctx runtime.SpecialContext, name string) string {
		return ctx.Package().Scopes[ctx.File()][name].Path
	}
	typeExpr := func(ctx runtime.SpecialContext, expr ast.Expr) string {
		if star, ok := expr.(*ast.StarExpr); ok {
			expr = star.X
		}
		sel := expr.(*ast.SelectorExpr)
		return importPathOf(ctx, sel.X.(*ast.Ident).Name) + "." + sel.Sel.Name
	}

	var records []string
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/define", Name: "Rule"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			sel := call.Call.Args[0].(*ast.SelectorExpr)
			records = append(records, "Rule "+importPathOf(ctx, sel.X.(*ast.Ident).Name)+"."+sel.Sel.Name)
			return runtime.NIL, nil
		})
	e.RegisterSpecial(runtime.SymbolID{PackagePath: "example.com/define", Name: "Convert"},
		func(ctx runtime.SpecialContext, call *runtime.QuotedCall) (runtime.Value, error) {
			fn := call.Call.Args[0].(*ast.FuncLit)
			dst := fn.Type.Params.List[1].Type
			src := fn.Type.Params.List[2].Type
			records = append(records, "Convert "+typeExpr(ctx, dst)+" <- "+typeExpr(ctx, src))
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ce, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := ce.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Map", "Convert", "Compute":
					var args []string
					for _, a := range ce.Args {
						args = append(args, ctx.Format(a))
					}
					records = append(records, "  "+sel.Sel.Name+"("+strings.Join(args, ", ")+")")
				}
				return true
			})
			return runtime.NIL, nil
		})

	pkg, err := e.LoadFile(ctx, "./testdata/dslfile/defs.go")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if _, err := e.Call(ctx, pkg, "main"); err != nil {
		t.Fatalf("main: %v", err)
	}

	want := []string{
		"Rule example.com/convutil.TimeToString",
		"Convert example.com/destination.DstUser <- example.com/source.SrcUser",
		"  Map(dst.UserID, src.ID)",
		"  Convert(dst.Contact, src.ContactInfo, convutil.ConvertContact)",
		"  Compute(dst.FullName, convutil.MakeFullName(src.FirstName, src.LastName))",
	}
	if diff := cmp.Diff(want, records); diff != "" {
		t.Errorf("records mismatch (-want +got):\n%s", diff)
	}
	// the laziness claim: quoting an import never locates the package —
	// define (special), convutil/source/destination (quoted args) never hit
	// the resolver at all.
	if len(spy.located) != 0 {
		t.Errorf("materialized packages: %v", spy.located)
	}
	if len(spy.dirs) != 0 {
		t.Errorf("directories located: %v", spy.dirs)
	}
}

func TestVet(t *testing.T) {
	e := newEngine(t)
	e.Bind("example.com/dsl", map[string]runtime.Value{
		"Registered": &runtime.BuiltinFunc{Name: "dsl.Registered", Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return nil, nil
		}},
	})
	got, err := e.Vet(context.Background(), "./testdata/vetmain")
	if err != nil {
		t.Fatalf("Vet: %v", err)
	}
	var resolved []string
	for _, r := range got {
		resolved = append(resolved, r.Resolved)
	}
	want := []string{
		"github.com/podhmo/minigo/testdata/vetstub.Unreg",
	}
	if diff := cmp.Diff(want, resolved); diff != "" {
		t.Errorf("findings mismatch (-want +got):\n%s", diff)
	}
	if len(got) == 1 && !strings.HasSuffix(got[0].Pos.Filename, "vetmain/main.go") {
		t.Errorf("finding position: got %v", got[0].Pos)
	}
}

type natPoint struct{ X int }

func TestNativeBindings(t *testing.T) {
	e := newEngine(t)
	e.Bind("example.com/nat", map[string]runtime.Value{
		"Add":       minigo.WrapFunc("nat.Add", func(a, b int) int { return a + b }),
		"Upper":     minigo.WrapFunc("nat.Upper", strings.ToUpper),
		"Concat":    minigo.WrapFunc("nat.Concat", func(parts ...string) string { return strings.Join(parts, "") }),
		"Pair":      minigo.WrapFunc("nat.Pair", func() (int, string) { return 7, "x" }),
		"Shift":     minigo.WrapFunc("nat.Shift", func(v uint) uint { return v << 1 }),
		"NonFunc":   minigo.WrapFunc("nat.NonFunc", 42),
		"Version":   minigo.ValueOf("v2"),
		"Join":      minigo.WrapFunc("nat.Join", strings.Join),
		"Count":     minigo.WrapFunc("nat.Count", func(m map[string]int) int { return m["k"] }),
		"MakePoint": minigo.WrapFunc("nat.MakePoint", func() natPoint { return natPoint{X: 5} }),
		"XOf":       minigo.WrapFunc("nat.XOf", func(p natPoint) int { return p.X }),
		"Point":     &runtime.TypeDef{Name: "Point", Kind: runtime.KindNamedBasic, Pkg: &runtime.Package{Path: "example.com/nat", Name: "nat"}},
	})
	cases := []struct {
		fn   string
		want runtime.Value
	}{
		{"Add", int64(3)},
		{"Upper", "GO"},
		{"Concat", "abc"},
		{"Pair", int64(7)},
		{"Shift", int64(6)},
		{"Version", "v2"},
		{"Join", "a-b"},
		{"Count", int64(3)},
		{"MkPoint", int64(5)},
	}
	// uint64 above MaxInt64 keeps its exact value boxed rather than
	// wrapping to a negative int64
	if got := minigo.ValueOf(uint64(math.MaxUint64)); true {
		if _, ok := got.(*runtime.GoValue); !ok {
			t.Errorf("ValueOf(MaxUint64) = %T, want *runtime.GoValue", got)
		}
	}
	if got := minigo.ValueOf(uint64(42)); got != int64(42) {
		t.Errorf("ValueOf(42) = %v, want int64", got)
	}
	for _, c := range cases {
		got := run(t, e, "./testdata/native", c.fn)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", c.fn, diff)
		}
	}
	// a WrapFunc over a non-function errors at call time, not bind time
	_, err := e.Run(context.Background(), "./testdata/native", "NonFunc")
	if err == nil || !strings.Contains(err.Error(), "not a function") {
		t.Errorf("NonFunc: want not-a-function error, got %v", err)
	}
}

// A package's init() side effects must be visible when its members are
// used — Go runs every imported package's init before use, and the
// default GoCompatibleInit mode reproduces that on first member access,
// including purely indirect chains (main -> helper -> inittable).
func TestInitOnMemberAccess(t *testing.T) {
	e := newEngine(t)
	for fn, want := range map[string]runtime.Value{
		"ViaFunc":     int64(5), // func init() populated Table before Lookup ran
		"ViaIndirect": int64(5), // same through an intermediate package
	} {
		got := run(t, e, "./testdata/inituser", fn)
		if got != want {
			t.Errorf("%s = %v, want %v", fn, got, want)
		}
	}
}
