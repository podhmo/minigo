package main

import (
	"bytes"
	"context"
	"errors"
	"go/constant"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

func writeTaskfile(t *testing.T, src string) (dir, file string) {
	t.Helper()
	dir = t.TempDir()
	file = filepath.Join(dir, "Taskfile.go")
	if err := os.WriteFile(file, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

func TestTasks(t *testing.T) {
	r := NewRunner("testdata", io.Discard, io.Discard)
	tasks, err := r.Tasks(context.Background(), filepath.Join("testdata", "Taskfile.go"))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]TaskInfo{}
	for _, ti := range tasks {
		byName[ti.Name] = ti
	}
	for _, name := range []string{"Default", "Lint", "Build", "Clean", "Dist", "Greet", "Paths"} {
		ti, ok := byName[name]
		if !ok {
			t.Errorf("task %s missing from -l list", name)
			continue
		}
		if ti.Doc == "" {
			t.Errorf("task %s: doc comment should become its description", name)
		}
	}
	if diff := cmp.Diff([]string{"name"}, byName["Greet"].Params); diff != "" {
		t.Errorf("Greet params (-want +got):\n%s", diff)
	}
	if _, ok := byName["Paths"]; !ok {
		t.Error("expected Paths task")
	}
}

func TestRunTask(t *testing.T) {
	var out, errb bytes.Buffer
	r := NewRunner("testdata", &out, &errb)
	tf := filepath.Join("testdata", "Taskfile.go")
	ctx := context.Background()

	if err := r.RunTask(ctx, tf, "Greet", []string{"world"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("hello world\n", out.String()); diff != "" {
		t.Errorf("Greet output (-want +got):\n%s", diff)
	}

	// Default -> Deps(Lint, Build): Lint logs to stderr, Build writes app.out
	artifact := filepath.Join("testdata", "app.out")
	defer os.Remove(artifact)
	if err := r.RunTask(ctx, tf, "Default", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errb.String(), "linting...") || !strings.Contains(errb.String(), "building...") {
		t.Errorf("expected lint+build logs, got %q", errb.String())
	}
	if b, err := os.ReadFile(artifact); err != nil || string(b) != "built\n" {
		t.Errorf("app.out: %q, %v", b, err)
	}
}

func TestDepsDedup(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func Setup() { task.Log("setup ran") }

func A() { task.Deps(Setup) }

func B() { task.Deps(Setup) }

func Default() { task.Deps(A, B, Setup) }
`)
	var errb bytes.Buffer
	r := NewRunner(filepath.Dir(file), io.Discard, &errb)
	if err := r.RunTask(context.Background(), file, "Default", nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(errb.String(), "setup ran"); n != 1 {
		t.Fatalf("Setup ran %d times, want 1 (deps dedup)", n)
	}
}

func TestDepsCycle(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func A() { task.Deps(B) }

func B() { task.Deps(A) }
`)
	r := NewRunner(filepath.Dir(file), io.Discard, io.Discard)
	err := r.RunTask(context.Background(), file, "A", nil)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a dependency-cycle error, got %v", err)
	}
}

// TestDepsCycleAcrossBranches: a cycle whose edges are claimed by sibling
// spawned goroutines is invisible to the spawn-ancestry check — it must
// be caught by the wait-time check, not deadlocked.
func TestDepsCycleAcrossBranches(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func Default() { task.Deps(A, B) }

func A() { task.Deps(B) }

func B() { task.Deps(A) }
`)
	r := NewRunner(filepath.Dir(file), io.Discard, io.Discard)
	done := make(chan error, 1)
	go func() { done <- r.RunTask(context.Background(), file, "Default", nil) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("expected a dependency-cycle error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cross-branch dependency cycle deadlocked instead of erroring")
	}
}

// TestDepsCycleAcrossBranches3: a three-dep cycle spread across sibling
// claims (A waits B, B waits C, C waits A) is detected the same way.
func TestDepsCycleAcrossBranches3(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func Default() { task.Deps(A, B, C) }

func A() { task.Deps(B) }

func B() { task.Deps(C) }

func C() { task.Deps(A) }
`)
	r := NewRunner(filepath.Dir(file), io.Discard, io.Discard)
	done := make(chan error, 1)
	go func() { done <- r.RunTask(context.Background(), file, "Default", nil) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("expected a dependency-cycle error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cross-branch dependency cycle deadlocked instead of erroring")
	}
}

// TestDepsDiamond: sibling deps sharing a dep is not a cycle — the
// shared dep must run once and both branches proceed.
func TestDepsDiamond(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func Default() { task.Deps(A, B) }

func A() { task.Deps(Shared) }

func B() { task.Deps(Shared) }

func Shared() { task.Log("shared ran") }
`)
	var errb bytes.Buffer
	r := NewRunner(filepath.Dir(file), io.Discard, &errb)
	done := make(chan error, 1)
	go func() { done <- r.RunTask(context.Background(), file, "Default", nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("diamond deps reported a cycle: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("diamond deps deadlocked")
	}
	if n := strings.Count(errb.String(), "shared ran"); n != 1 {
		t.Fatalf("Shared ran %d times, want 1 (dedup)", n)
	}
}

func TestDepsWithArgs(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func Emit(msg string) { task.Log(msg) }

func Default() {
	task.Deps(task.F(Emit, "a"), task.F(Emit, "a"), task.F(Emit, "b"))
}
`)
	var errb bytes.Buffer
	r := NewRunner(filepath.Dir(file), io.Discard, &errb)
	if err := r.RunTask(context.Background(), file, "Default", nil); err != nil {
		t.Fatal(err)
	}
	// F(Emit,"a") twice dedups to one run; F(Emit,"b") runs once —
	// parallel deps, so only the multiset is deterministic
	if diff := cmp.Diff([]string{"a", "b"}, sortedLines(errb.String())); diff != "" {
		t.Errorf("deps output (-want +got):\n%s", diff)
	}
}

func TestShAndTarget(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import (
	"os"
	"task"
)

func Write() error {
	return task.Sh("echo content > gen.txt")
}

func Check() error {
	ok, err := task.Target("gen.txt", "Taskfile.go")
	if err != nil {
		return err
	}
	if ok {
		return os.WriteFile("verdict.txt", "fresh", 0644)
	}
	return os.WriteFile("verdict.txt", "stale", 0644)
}
`)
	dir := filepath.Dir(file)
	r := NewRunner(dir, io.Discard, io.Discard)
	ctx := context.Background()
	if err := r.RunTask(ctx, file, "Write", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gen.txt")); err != nil {
		t.Fatalf("task.Sh did not run in the taskfile's directory: %v", err)
	}
	if err := r.RunTask(ctx, file, "Check", nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "verdict.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "fresh" {
		t.Fatalf("target should be up to date, verdict=%q", b)
	}
}

func TestRunOutput(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import (
	"fmt"
	"task"
)

func Default() {
	v, err := task.Output("echo", "cap")
	if err != nil {
		task.Log("output:", err)
		return
	}
	fmt.Println("got:", v)
}
`)
	var out bytes.Buffer
	r := NewRunner(filepath.Dir(file), &out, io.Discard)
	if err := r.RunTask(context.Background(), file, "Default", nil); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("got: cap\n", out.String()); diff != "" {
		t.Errorf("Output mismatch (-want +got):\n%s", diff)
	}
}

func TestTaskErrors(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import (
	"errors"
	"task"
)

func Failing() error {
	return errors.New("boom")
}

func helper() {}

func Calc() int { return 0 }
`)
	r := NewRunner(filepath.Dir(file), io.Discard, io.Discard)
	ctx := context.Background()

	if err := r.RunTask(ctx, file, "Failing", nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("task error must propagate, got %v", err)
	}
	if err := r.RunTask(ctx, file, "helper", nil); err == nil || !strings.Contains(err.Error(), "no task") {
		t.Fatalf("unexported helper is not a task, got %v", err)
	}
	if err := r.RunTask(ctx, file, "Calc", nil); err == nil || !strings.Contains(err.Error(), "not a task") {
		t.Fatalf("wrong-signature func is not a task, got %v", err)
	}
}

func TestRunMainList(t *testing.T) {
	var out bytes.Buffer
	code := runMain(context.Background(), []string{"-f", filepath.Join("testdata", "Taskfile.go"), "-l"}, &out, io.Discard)
	if code != 0 {
		t.Fatalf("runMain -l: code %d", code)
	}
	if !strings.Contains(out.String(), "Default()") || !strings.Contains(out.String(), "Greet(name)") {
		t.Fatalf("list output missing tasks:\n%s", out.String())
	}
}

func TestOutputError(t *testing.T) {
	// task.Output must return (string, error) even on failure — a bare
	// error value would break the two-value assignment in script
	_, file := writeTaskfile(t, `package main

import "task"

func FailCap() {
	v, err := task.Output("false")
	if err != nil {
		task.Log("caught:", v == "")
		return
	}
	task.Log("v:", v)
}
`)
	var errb bytes.Buffer
	r := NewRunner(filepath.Dir(file), io.Discard, &errb)
	if err := r.RunTask(context.Background(), file, "FailCap", nil); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("caught: true\n", errb.String()); diff != "" {
		t.Errorf("Output error path (-want +got):\n%s", diff)
	}
}

// TestStrictStringArgs: args declared `string` in the stub reject
// non-string values using script-side type names, rather than being
// silently stringified into a misleading exec error.
func TestStrictStringArgs(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func RunInt() { task.Run(42) }

func ShTwo() { task.Sh("a", "b") }

func RunNamed() error {
	type Str string
	return task.Run(Str("echo"), "named-ok")
}
`)
	r := NewRunner(filepath.Dir(file), io.Discard, io.Discard)
	ctx := context.Background()

	err := r.RunTask(ctx, file, "RunInt", nil)
	if err == nil || !strings.Contains(err.Error(), "task.Run arg 1 must be a string, got int") {
		t.Fatalf("Run(42) should be a type error in the script's vocabulary, got %v", err)
	}
	err = r.RunTask(ctx, file, "ShTwo", nil)
	if err == nil || !strings.Contains(err.Error(), "task.Sh takes 1 arg, got 2") {
		t.Fatalf("extra Sh arg should not be silently dropped, got %v", err)
	}
	// a defined string type still unwraps to a string
	if err := r.RunTask(ctx, file, "RunNamed", nil); err != nil {
		t.Fatalf("named string arg should be accepted, got %v", err)
	}
}

// TestConstStringArgs: `const N = "echo"` is a valid `string` arg in Go —
// untyped constants materialize at the call boundary, so the strict
// check must unwrap *runtime.UConst instead of rejecting the call as
// `got *runtime.UConst`.
func TestConstStringArgs(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

const N = "echo"
const Cmd = "echo const-sh-ok"
const Dir = "."
const F = "Taskfile.go"
const Typed string = "echo"

type S string

const Named S = "echo"

func RunConst() error { return task.Run(N, "-n", "const-run-ok") }

func ShConst() error { return task.Sh(Cmd) }

func RunInConst() error { return task.RunIn(Dir, N, "-n", "const-in-ok") }

func RunTyped() error { return task.Run(Typed, "-n", "const-typed-ok") }

func RunNamedConst() error { return task.Run(Named, "-n", "const-named-ok") }

func TargetConst() error {
	ok, _ := task.Target(F)
	if !ok {
		task.Log("target should exist:", F)
	}
	return nil
}
`)
	var out, errb bytes.Buffer
	r := NewRunner(filepath.Dir(file), &out, &errb)
	ctx := context.Background()
	for _, name := range []string{"RunConst", "ShConst", "RunInConst", "RunTyped", "RunNamedConst", "TargetConst"} {
		if err := r.RunTask(ctx, file, name, nil); err != nil {
			t.Errorf("%s: untyped/typed/named string consts must be accepted, got %v", name, err)
		}
	}
	got := out.String()
	for _, want := range []string{"const-run-ok", "const-sh-ok", "const-in-ok", "const-typed-ok", "const-named-ok"} {
		if !strings.Contains(got, want) {
			t.Errorf("const arg should have reached the command: want %q in\n%s", want, got)
		}
	}
}

// TestStrValUConst: the unit-level half of the const fix — strVal peels
// Named then materializes string UConsts; non-string UConsts still fail.
func TestStrValUConst(t *testing.T) {
	if s, ok := strVal(&runtime.UConst{V: constant.MakeString("x")}); !ok || s != "x" {
		t.Errorf("string UConst should materialize, got %q, %v", s, ok)
	}
	if s, ok := strVal(&runtime.UConst{V: constant.MakeInt64(1)}); ok {
		t.Errorf("int UConst is not a string arg, got %q", s)
	}
	// kindOf names a rejected UConst by its Go default type
	if got := kindOf(&runtime.UConst{V: constant.MakeInt64(1)}); got != "int" {
		t.Errorf("kindOf(UConst int) = %q, want int", got)
	}
}

// TestCmdLabel: command-line boundaries stay readable — plain args bare,
// space/quote-bearing args %q-quoted like task.Sh's `sh -c %q` spelling.
func TestCmdLabel(t *testing.T) {
	if got := cmdLabel("", "sh", []string{"-c", "exit 3"}); got != `sh -c "exit 3"` {
		t.Errorf("cmdLabel sh = %q", got)
	}
	if got := cmdLabel("", "echo", []string{"hello", "world"}); got != "echo hello world" {
		t.Errorf("cmdLabel echo = %q", got)
	}
	if got := cmdLabel("sub", "go", []string{"build", "./..."}); got != "(in sub) go build ./..." {
		t.Errorf("cmdLabel dir = %q", got)
	}
	if got := cmdLabel("", "echo", []string{""}); got != `echo ""` {
		t.Errorf("cmdLabel empty arg = %q", got)
	}
}

// TestExitErrLabel: a non-zero exit names the command the script spelled,
// not just "exit status N".
func TestExitErrLabel(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func Boom() error { return task.Sh("false") }
`)
	r := NewRunner(filepath.Dir(file), io.Discard, io.Discard)
	err := r.RunTask(context.Background(), file, "Boom", nil)
	if err == nil || !strings.Contains(err.Error(), `sh -c "false": exit status 1`) {
		t.Fatalf("exit error should name the spelled command, got %v", err)
	}
}

// TestTaskfileErr: LoadFile failures blame the Taskfile or the -f flag —
// classified by who must fix them, never prefixed with the task name.
func TestTaskfileErr(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(dir, io.Discard, io.Discard)
	r.explicitFile = true // simulates an explicit -f from main
	ctx := context.Background()

	err := r.RunTask(ctx, filepath.Join(dir, "Nope.go"), "Default", nil)
	if err == nil || !strings.Contains(err.Error(), "does not exist. Fix the -f argument") {
		t.Fatalf("missing taskfile should blame the -f argument, got %v", err)
	}
	var le *loadError
	if !errors.As(err, &le) {
		t.Fatal("missing taskfile error should be a *loadError")
	}

	bad := filepath.Join(dir, "Bad.go")
	if werr := os.WriteFile(bad, []byte("package main\nfunc {"), 0644); werr != nil {
		t.Fatal(werr)
	}
	err = r.RunTask(ctx, bad, "Default", nil)
	if err == nil || !strings.Contains(err.Error(), "Fix the Taskfile") {
		t.Fatalf("parse failure should blame the Taskfile, got %v", err)
	}

	// a directory is not a taskfile — still the -f argument's fault
	err = r.RunTask(ctx, dir, "Default", nil)
	if err == nil || !strings.Contains(err.Error(), "is a directory. Fix the -f argument") {
		t.Fatalf("directory taskfile should blame the -f argument, got %v", err)
	}

	// without -f there is no -f argument to fix: the default file's
	// advice is to create it or pass -f — never "fix the -f argument"
	r2 := NewRunner(dir, io.Discard, io.Discard)
	err = r2.RunTask(ctx, filepath.Join(dir, "Taskfile.go"), "Default", nil)
	if err == nil || !strings.Contains(err.Error(), "does not exist. Pass -f") {
		t.Fatalf("missing default taskfile should not blame a -f that was never given, got %v", err)
	}
}

// TestRunMainLoadErr: a load failure prints without the "task Default:"
// prefix — the named task never ran.
func TestRunMainLoadErr(t *testing.T) {
	dir := t.TempDir()
	var errb bytes.Buffer
	code := runMain(context.Background(), []string{"-f", filepath.Join(dir, "Nope.go")}, io.Discard, &errb)
	if code != 1 {
		t.Fatalf("runMain missing file: code %d", code)
	}
	if strings.Contains(errb.String(), "task ") || !strings.Contains(errb.String(), "does not exist") {
		t.Fatalf("load error should not be prefixed with a task name: %q", errb.String())
	}
}

// TestRunMainDefaultFileErr: with no -f flag at all, a missing default
// Taskfile.go must not say "fix the -f argument" — there was none.
func TestRunMainDefaultFileErr(t *testing.T) {
	t.Chdir(t.TempDir()) // cwd has no Taskfile.go
	var errb bytes.Buffer
	code := runMain(context.Background(), []string{"-l"}, io.Discard, &errb)
	if code != 1 {
		t.Fatalf("runMain missing default file: code %d", code)
	}
	got := errb.String()
	if !strings.Contains(got, "Taskfile.go does not exist") || strings.Contains(got, "Fix the -f") {
		t.Fatalf("missing default taskfile should not blame -f: %q", got)
	}
}

// sortedLines splits buf's output into sorted lines for order-insensitive
// comparisons under parallel deps.
func sortedLines(s string) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	sort.Strings(lines)
	return lines
}

// TestDepsParallel: deps handshake across shared channels — A signals B
// before B signals back, which serial deps could never satisfy (A's send
// would park forever). Completion proves real goroutine interleaving.
func TestDepsParallel(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

var a2b = make(chan int)
var b2a = make(chan int)

func A() { a2b <- 1; <-b2a }
func B() { <-a2b; b2a <- 1 }

func Default() { task.Deps(A, B) }
`)
	r := NewRunner(filepath.Dir(file), io.Discard, io.Discard)
	if err := r.RunTask(context.Background(), file, "Default", nil); err != nil {
		t.Fatalf("parallel deps handshake failed: %v", err)
	}
}

// TestDepsParallelDedupAcrossModes: a dep claimed by parallel Deps is
// also deduped for a later SerialDeps — the depStates map is shared.
func TestDepsParallelDedupAcrossModes(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func Setup() { task.Log("setup ran") }

func Default() {
	task.Deps(Setup)
	task.SerialDeps(Setup)
}
`)
	var errb bytes.Buffer
	r := NewRunner(filepath.Dir(file), io.Discard, &errb)
	if err := r.RunTask(context.Background(), file, "Default", nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(errb.String(), "setup ran"); n != 1 {
		t.Fatalf("Setup ran %d times, want 1 (cross-mode dedup)", n)
	}
}

// TestSerialDepsOrder: SerialDeps keeps serial ordering on the caller's
// goroutine — output order is deterministic.
func TestSerialDepsOrder(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func A() { task.Log("a") }
func B() { task.Log("b") }

func Default() { task.SerialDeps(A, B) }
`)
	var errb bytes.Buffer
	r := NewRunner(filepath.Dir(file), io.Discard, &errb)
	if err := r.RunTask(context.Background(), file, "Default", nil); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff("a\nb\n", errb.String()); diff != "" {
		t.Errorf("serial deps output (-want +got):\n%s", diff)
	}
}

// TestDepsParallelFailure: a failing parallel dep fails the whole run —
// its error result becomes a call error that kills the process.
func TestDepsParallelFailure(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import (
	"errors"
	"task"
)

func Bad() error { return errors.New("dep broke") }

func Default() { task.Deps(Bad) }
`)
	r := NewRunner(filepath.Dir(file), io.Discard, io.Discard)
	if err := r.RunTask(context.Background(), file, "Default", nil); err == nil || !strings.Contains(err.Error(), "dep broke") {
		t.Fatalf("expected dep failure to fail the run, got %v", err)
	}
}

func TestDepsArgKeyNoCollision(t *testing.T) {
	// F(Emit2,"a b","c") and F(Emit2,"a","b c") must not dedup-collapse
	_, file := writeTaskfile(t, `package main

import "task"

func Emit2(x, y string) { task.Log(x + "|" + y) }

func Default() {
	task.Deps(task.F(Emit2, "a b", "c"), task.F(Emit2, "a", "b c"))
}
`)
	var errb bytes.Buffer
	r := NewRunner(filepath.Dir(file), io.Discard, &errb)
	if err := r.RunTask(context.Background(), file, "Default", nil); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"a b|c", "a|b c"}, sortedLines(errb.String())); diff != "" {
		t.Errorf("dedup collision (-want +got):\n%s", diff)
	}
}

func TestTaskShapeEdgeCases(t *testing.T) {
	_, file := writeTaskfile(t, `package main

import "task"

func Pos(string) { task.Log("got arg") }

func Two() (a, b error) { return nil, nil }
`)
	var errb bytes.Buffer
	r := NewRunner(filepath.Dir(file), io.Discard, &errb)
	ctx := context.Background()

	// unnamed parameter still requires its arg
	if err := r.RunTask(ctx, file, "Pos", []string{"x"}); err != nil {
		t.Fatalf("Pos with arg: %v", err)
	}
	if diff := cmp.Diff("got arg\n", errb.String()); diff != "" {
		t.Errorf("Pos output (-want +got):\n%s", diff)
	}
	if err := r.RunTask(ctx, file, "Pos", nil); err == nil || !strings.Contains(err.Error(), "takes 1") {
		t.Fatalf("Pos without arg should demand 1, got %v", err)
	}
	// two error results is not a task
	if err := r.RunTask(ctx, file, "Two", nil); err == nil || !strings.Contains(err.Error(), "not a task") {
		t.Fatalf("Two() (a, b error) must not be a task, got %v", err)
	}
}
