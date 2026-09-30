package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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
	// F(Emit,"a") twice dedups to one run; F(Emit,"b") runs once
	if diff := cmp.Diff("a\nb\n", errb.String()); diff != "" {
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
	if diff := cmp.Diff("a b|c\na|b c\n", errb.String()); diff != "" {
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
