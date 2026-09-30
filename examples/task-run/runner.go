package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/podhmo/minigo"
	"github.com/podhmo/minigo/runtime"
)

// Import paths a Taskfile may spell to reach the bound `task` package:
// the short name keeps taskfiles readable, the module path is what real
// Go tooling (gopls, go vet) resolves to task/task.go.
const (
	taskPkgPath = "task"
	taskPkgMod  = "github.com/podhmo/minigo/examples/task-run/task"
)

// taskCall is the wrapped call produced by task.F — a function plus its
// bound arguments, deduplicated by identity+args in Deps.
type taskCall struct {
	fn   runtime.Value
	args []runtime.Value
}

// TaskInfo describes one task for `task-run -l`.
type TaskInfo struct {
	Name   string
	Params []string // parameter names, in order (task-run <task> args)
	Doc    string   // first doc-comment line
}

// Runner executes Taskfiles through a minigo engine: the script runs
// inside the interpreter, while the `task` package calls back into this
// host process to spawn commands and query files.
type Runner struct {
	engine *minigo.Engine
	stdout io.Writer
	stderr io.Writer

	ran     map[string]bool // dep tasks already run (deps dedup)
	running map[string]bool // in-flight dep tasks (cycle detection)
}

// NewRunner creates a runner whose working directory is dir — scripts and
// subprocesses alike see it as their cwd (the engine's virtual cwd plus
// cmd.Dir on every spawned command).
func NewRunner(dir string, stdout, stderr io.Writer) *Runner {
	r := &Runner{
		stdout:  stdout,
		stderr:  stderr,
		ran:     map[string]bool{},
		running: map[string]bool{},
	}
	e := minigo.NewEngine(dir,
		minigo.WithWorkingDir(dir),
		minigo.WithOutput(stdout),
	)
	r.engine = e
	tb := r.taskBinds()
	for _, path := range []string{taskPkgPath, taskPkgMod} {
		e.Bind(path, tb)
	}
	return r
}

// Tasks loads a Taskfile and lists its tasks: exported top-level functions
// whose params are all `string` and whose result is empty or `error`.
func (r *Runner) Tasks(ctx context.Context, file string) ([]TaskInfo, error) {
	pkg, err := r.engine.LoadFile(ctx, file)
	if err != nil {
		return nil, err
	}
	var tasks []TaskInfo
	for name, d := range pkg.Index.Funcs {
		fd := d.Func
		if fd == nil || !ast.IsExported(name) {
			continue
		}
		params, ok := taskShape(fd.Type)
		if !ok {
			continue // helper functions and constants are not tasks
		}
		doc := ""
		if fd.Doc != nil {
			doc, _, _ = strings.Cut(fd.Doc.Text(), "\n")
			doc = strings.TrimSpace(doc)
		}
		tasks = append(tasks, TaskInfo{Name: name, Params: params, Doc: doc})
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })
	return tasks, nil
}

// RunTask invokes one task by name. Positional args feed the task's string
// parameters; a task `func Deploy(env string)` runs as `task-run Deploy dev`.
func (r *Runner) RunTask(ctx context.Context, file, name string, args []string) error {
	pkg, err := r.engine.LoadFile(ctx, file)
	if err != nil {
		return err
	}
	d, ok := pkg.Index.Funcs[name]
	if !ok || !ast.IsExported(name) {
		return fmt.Errorf("no task %q (run with -l to list)", name)
	}
	params, ok := taskShape(d.Func.Type)
	if !ok {
		return fmt.Errorf("%s is not a task (params must all be string, result empty or error)", name)
	}
	if len(args) != len(params) {
		return fmt.Errorf("takes %d args (%s), got %d", len(params), strings.Join(params, ", "), len(args))
	}
	argv := make([]runtime.Value, len(args))
	for i, a := range args {
		argv[i] = a
	}
	res, err := r.engine.Call(ctx, pkg, name, argv...)
	if err != nil {
		return err
	}
	return resultErr(res)
}

// resultErr unwraps a task's error result (a `func() error` task returns
// its error as a boxed host value).
func resultErr(v runtime.Value) error {
	switch x := v.(type) {
	case *runtime.GoValue:
		if err, ok := x.V.(error); ok {
			return err
		}
	case *runtime.Tuple:
		// a task returning multiple values: last element is the error
		if len(x.Elems) > 0 {
			return resultErr(x.Elems[len(x.Elems)-1])
		}
	}
	return nil
}

// taskShape validates the task signature and returns parameter names:
// every param must be `string`, and results must be empty or one `error`.
func taskShape(ft *ast.FuncType) ([]string, bool) {
	if ft.TypeParams != nil && len(ft.TypeParams.List) > 0 {
		return nil, false
	}
	var names []string
	if ft.Params != nil {
		for _, f := range ft.Params.List {
			id, ok := f.Type.(*ast.Ident)
			if !ok || id.Name != "string" {
				return nil, false
			}
			if len(f.Names) == 0 {
				// an unnamed parameter still occupies a call slot
				names = append(names, "arg")
			}
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
		}
	}
	if ft.Results != nil {
		nres := 0
		for _, f := range ft.Results.List {
			if len(f.Names) == 0 {
				nres++
			} else {
				nres += len(f.Names)
			}
		}
		if nres != 1 {
			return nil, false
		}
		id, ok := ft.Results.List[0].Type.(*ast.Ident)
		if !ok || id.Name != "error" {
			return nil, false
		}
	}
	return names, true
}

// ---- task package intrinsics ----

func (r *Runner) taskBinds() map[string]runtime.Value {
	return map[string]runtime.Value{
		"Deps":       &runtime.BuiltinFunc{Name: "task.Deps", Fn: r.deps},
		"SerialDeps": &runtime.BuiltinFunc{Name: "task.SerialDeps", Fn: r.deps},
		"F": &runtime.BuiltinFunc{Name: "task.F", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) == 0 {
				return nil, errors.New("task.F needs a task function")
			}
			return &runtime.GoValue{V: taskCall{fn: args[0], args: args[1:]}}, nil
		}},
		"Sh": &runtime.BuiltinFunc{Name: "task.Sh", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			cmdline, err := strArg("task.Sh", args, 0)
			if err != nil {
				return nil, err
			}
			cmd := exec.Command("sh", "-c", cmdline)
			cmd.Dir = r.engine.WorkingDir()
			cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
			return errOf(cmd.Run())
		}},
		"Run": &runtime.BuiltinFunc{Name: "task.Run", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return errOf(r.runCmd("", args, "task.Run"))
		}},
		"RunIn": &runtime.BuiltinFunc{Name: "task.RunIn", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			dir, err := strArg("task.RunIn", args, 0)
			if err != nil {
				return nil, err
			}
			return errOf(r.runCmd(dir, args[1:], "task.RunIn"))
		}},
		"Output": &runtime.BuiltinFunc{Name: "task.Output", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			name, err := strArg("task.Output", args, 0)
			if err != nil {
				return nil, err
			}
			cmd := exec.Command(name, strArgs(args[1:])...)
			cmd.Dir = r.engine.WorkingDir()
			cmd.Stderr = r.stderr
			out, err := cmd.Output()
			ev, _ := errOf(err)
			return &runtime.Tuple{Elems: []runtime.Value{strings.TrimRight(string(out), "\n"), ev}}, nil
		}},
		"Target": &runtime.BuiltinFunc{Name: "task.Target", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return r.target(args)
		}},
		"Log": &runtime.BuiltinFunc{Name: "task.Log", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			parts := make([]any, len(args))
			for i, a := range args {
				parts[i] = strOf(a)
			}
			fmt.Fprintln(r.stderr, parts...)
			return runtime.NIL, nil
		}},
		"Env": &runtime.BuiltinFunc{Name: "task.Env", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			name, err := strArg("task.Env", args, 0)
			if err != nil {
				return nil, err
			}
			return os.Getenv(name), nil
		}},
	}
}

// deps implements task.Deps/task.SerialDeps: every argument is a task
// function (or a task.F-wrapped call) invoked once per runner invocation.
// Deps run sequentially — minigo's `go` statement is synchronous, so
// parallel deps are a documented approximation, not a race.
func (r *Runner) deps(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	for _, a := range args {
		if err := r.callDep(v, a); err != nil {
			return nil, err
		}
	}
	return runtime.NIL, nil
}

func (r *Runner) callDep(v runtime.VMCaller, a runtime.Value) error {
	fn, fargs, key, label := depOf(a)
	if key == "" {
		return fmt.Errorf("task.Deps: cannot use %T as a dependency", a)
	}
	if r.ran[key] {
		return nil
	}
	if r.running[key] {
		return fmt.Errorf("task.Deps: dependency cycle at %s", label)
	}
	r.running[key] = true
	defer delete(r.running, key)
	res, err := v.Call(fn, fargs)
	if err != nil {
		return fmt.Errorf("dep %s: %w", label, err)
	}
	if err := resultErr(res); err != nil {
		return fmt.Errorf("dep %s: %w", label, err)
	}
	r.ran[key] = true
	return nil
}

// depOf unwraps a Deps argument into (callable, args, dedup key, label).
func depOf(a runtime.Value) (runtime.Value, []runtime.Value, string, string) {
	if n, ok := a.(*runtime.Named); ok {
		a = n.V
	}
	switch x := a.(type) {
	case *runtime.GoValue:
		if c, ok := x.V.(taskCall); ok {
			// dedup key: length-prefixed args so distinct arg lists can't collide
			key := fmt.Sprintf("%p", c.fn)
			for _, a := range c.args {
				s := strOf(a)
				key += fmt.Sprintf("|%d:%s", len(s), s)
			}
			return c.fn, c.args, key, depLabel(c.fn)
		}
	case *runtime.Function:
		return x, nil, fmt.Sprintf("%p", x), x.Name
	case *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return x, nil, fmt.Sprintf("%p", x), fmt.Sprintf("%T", x)
	}
	return nil, nil, "", ""
}

func depLabel(fn runtime.Value) string {
	if f, ok := fn.(*runtime.Function); ok {
		return f.Name
	}
	return fmt.Sprintf("%T", fn)
}

// runCmd executes a program with stdio on the runner's streams. dir==""
// uses the engine's virtual cwd.
func (r *Runner) runCmd(dir string, args []runtime.Value, label string) error {
	name, err := strArg(label, args, 0)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, strArgs(args[1:])...)
	if dir == "" {
		cmd.Dir = r.engine.WorkingDir()
	} else if filepath.IsAbs(dir) {
		cmd.Dir = dir
	} else {
		cmd.Dir = filepath.Join(r.engine.WorkingDir(), dir)
	}
	cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
	return cmd.Run()
}

// target implements task.Target: target is up to date when it exists and
// is newer than (or equal to) every dep file's mtime.
func (r *Runner) target(args []runtime.Value) (runtime.Value, error) {
	if len(args) == 0 {
		return nil, errors.New("task.Target needs a target path")
	}
	t := r.resolvePath(strOf(args[0]))
	st, err := os.Stat(t)
	if err != nil {
		if os.IsNotExist(err) {
			return &runtime.Tuple{Elems: []runtime.Value{false, runtime.NIL}}, nil
		}
		return &runtime.Tuple{Elems: []runtime.Value{false, &runtime.GoValue{V: err}}}, nil
	}
	mtime := st.ModTime()
	for _, d := range args[1:] {
		dp := r.resolvePath(strOf(d))
		ds, err := os.Stat(dp)
		if err != nil {
			return &runtime.Tuple{Elems: []runtime.Value{false, &runtime.GoValue{V: err}}}, nil
		}
		if ds.ModTime().After(mtime) {
			return &runtime.Tuple{Elems: []runtime.Value{false, runtime.NIL}}, nil
		}
	}
	return &runtime.Tuple{Elems: []runtime.Value{true, runtime.NIL}}, nil
}

func (r *Runner) resolvePath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(r.engine.WorkingDir(), p)
}

// ---- marshalling helpers ----

// strOf renders a script argument as a string for command lines.
func strOf(v runtime.Value) string {
	switch x := v.(type) {
	case nil:
		return ""
	case *runtime.Named:
		return strOf(x.V)
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case *runtime.GoValue:
		return fmt.Sprint(x.V)
	default:
		return fmt.Sprint(x)
	}
}

func strArg(label string, args []runtime.Value, i int) (string, error) {
	if i >= len(args) {
		return "", fmt.Errorf("%s needs arg %d", label, i+1)
	}
	return strOf(args[i]), nil
}

func strArgs(args []runtime.Value) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strOf(a)
	}
	return out
}

// errOf marshals a host error to the script-visible (err) convention.
func errOf(err error) (runtime.Value, error) {
	if err != nil {
		return &runtime.GoValue{V: err}, nil
	}
	return runtime.NIL, nil
}
