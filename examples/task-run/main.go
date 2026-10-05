// Command task-run executes Taskfiles through the minigo interpreter —
// a mage/go-task style task runner where tasks are plain Go functions in
// a script file and the `task` package is bound into the engine.
//
//	task-run [-f Taskfile.go] -l          list tasks
//	task-run [-f Taskfile.go] [task ...]  run tasks (default: Default)
//	task-run [-f Taskfile.go] -n [task ...]  print commands without running them
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	os.Exit(runMain(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("task-run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", "Taskfile.go", "task file to load")
	list := fs.Bool("l", false, "list tasks and exit")
	dryRun := fs.Bool("n", false, "dry run: print commands and file mutations instead of executing them")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	explicitF := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "f" {
			explicitF = true
		}
	})

	abs, err := filepath.Abs(*file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	r := NewRunner(filepath.Dir(abs), stdout, stderr)
	r.explicitFile = explicitF
	if *dryRun {
		if err := r.SetDryRun(ctx); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}

	if *list {
		tasks, err := r.Tasks(ctx, abs)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for _, t := range tasks {
			sig := t.Name + "(" + strings.Join(t.Params, ", ") + ")"
			if t.Doc != "" {
				fmt.Fprintf(stdout, "%-24s %s\n", sig, t.Doc)
			} else {
				fmt.Fprintln(stdout, sig)
			}
		}
		return 0
	}

	names := fs.Args()
	if len(names) == 0 {
		names = []string{"Default"}
	}
	for _, spec := range names {
		// task args follow the name, colon-separated (task-run Deploy:prod)
		name, args := spec, []string(nil)
		if i := strings.IndexByte(spec, ':'); i >= 0 {
			name, args = spec[:i], strings.Split(spec[i+1:], ",")
		}
		if err := r.RunTask(ctx, abs, name, args); err != nil {
			// load errors blame the Taskfile/-f flag, not the task name
			var le *loadError
			if errors.As(err, &le) {
				fmt.Fprintln(stderr, err)
			} else {
				fmt.Fprintf(stderr, "task %s: %v\n", name, err)
			}
			return 1
		}
	}
	return 0
}
