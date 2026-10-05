//go:build task

// Demo Taskfile for task-run: tasks are exported Go functions; the doc
// comment becomes the `-l` description.

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/podhmo/minigo/examples/task-run/task"
)

// Default runs the full pipeline: lint, then build.
func Default() {
	task.Deps(Lint, Build)
	fmt.Println("done")
}

// Lint runs the formatter check and a quick vet (simulated).
func Lint() {
	task.Log("linting...")
	if err := task.Sh("echo 'gofmt ok'"); err != nil {
		task.Log("lint failed:", err)
	}
}

// Build writes the build artifact into app.out.
func Build() error {
	task.Log("building...")
	v, err := task.Output("go", "version")
	if err != nil {
		return err
	}
	task.Log("compiler:", v)
	return os.WriteFile("app.out", "built\n", 0644)
}

// Clean removes the build artifact.
func Clean() {
	task.Log("cleaning...")
	if err := os.Remove("app.out"); err != nil && !os.IsNotExist(err) {
		task.Log("clean failed:", err)
	}
}

// Dist builds only when app.out is older than this Taskfile.
func Dist() error {
	ok, err := task.Target("app.out", "Taskfile.go")
	if err != nil {
		return err
	}
	if ok {
		task.Log("app.out is up to date")
		return nil
	}
	return Build()
}

// Greet prints a greeting for the named target — `task-run Greet:world`.
func Greet(name string) {
	fmt.Println("hello", name)
}

// Paths lists the .go sources under the Taskfile's directory.
func Paths() {
	m, err := filepath.Glob("*.go")
	if err != nil {
		task.Log("glob:", err)
		return
	}
	for _, p := range m {
		fmt.Println("src:", p)
	}
}

// Info prints the target platform via os/exec — plain Go subprocesses
// work too, not only the task.* helpers.
func Info() error {
	cmd := exec.Command("go", "env", "GOOS", "GOARCH")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
