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
	"sync"

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

	mu        sync.Mutex
	depStates map[string]*depState        // dep key -> lifecycle (dedup + cycles)
	waits     map[*runtime.Task]*depState // task -> dep it is currently blocked on
}

// syncWriter serializes writes across dep goroutines: parallel deps
// share the runner's streams, and Fprintln's single Write per call keeps
// whole lines intact.
type syncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// depState is one dep's lifecycle: claimed once per key, run either on a
// spawned goroutine (task.Deps: task) or inline on the claimant's
// goroutine (task.SerialDeps: done/err). owner records the claimant's
// own task so a waiting goroutine can recognize when awaiting the claim
// would deadlock — dep A awaiting dep B which transitively awaits A.
type depState struct {
	label string
	owner *runtime.Task // claimant's task (nil = root goroutine)
	task  *runtime.Task // spawned dep goroutine (parallel Deps only)
	done  chan struct{} // serial claims signal completion here
	err   error         // outcome; set before done closes / task finishes
}

// wait blocks until the dep's outcome is known.
func (st *depState) wait() error {
	if st.task != nil {
		return st.task.Wait()
	}
	<-st.done
	return st.err
}

// finished reports whether the dep's outcome is already known without
// blocking — a closed task Done or serial done channel. Used to stop a
// wait-chain walk at stale edges left by tasks that died mid-wait.
func (st *depState) finished() bool {
	if st.task != nil {
		select {
		case <-st.task.Done:
			return true
		default:
			return false
		}
	}
	select {
	case <-st.done:
		return true
	default:
		return false
	}
}

// waiter returns the task a second claimant would wait on: the spawned
// dep goroutine, or the claiming task itself for a serial claim.
func (st *depState) waiter() *runtime.Task {
	if st.task != nil {
		return st.task
	}
	return st.owner
}

// taskInAncestry reports whether t is cur or an ancestor of cur — waiting
// on such a claim would deadlock (a dependency cycle).
func taskInAncestry(t, cur *runtime.Task) bool {
	for c := cur; c != nil; c = c.Parent {
		if c == t {
			return true
		}
	}
	return false
}

// claimDep registers key as in-flight and reports whether the caller must
// run it. A serial claim (spawn == nil) waits on st.done; a parallel
// claim spawns under the lock so no waiter can observe a task-less claim.
func (r *Runner) claimDep(key string, cur *runtime.Task, label string, spawn func() *runtime.Task) (*depState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st, ok := r.depStates[key]; ok {
		return st, false
	}
	st := &depState{label: label, owner: cur, done: make(chan struct{})}
	if spawn != nil {
		st.task = spawn()
	}
	r.depStates[key] = st
	return st, true
}

// await blocks cur on st like st.wait, but first registers the pending
// wait edge: if the dep's own wait chain leads back to cur, the wait
// would deadlock — reported as a dependency cycle instead. The claim-time
// ancestry check only sees the spawn tree, so sibling spawned goroutines
// that wait on each other (task.Deps(A, B) with A->B and B->A) can only
// be caught here, at wait time.
func (r *Runner) await(cur *runtime.Task, st *depState, label string) error {
	r.mu.Lock()
	if st.finished() {
		r.mu.Unlock()
		return st.wait()
	}
	r.waits[cur] = st
	// Walk the wait chain from the dep cur is about to block on: if it
	// leads back to cur, blocking would deadlock — report a cycle.
	labels := []string{st.label}
	cyclic := false
	for w := st.waiter(); w != nil; {
		if w == cur {
			cyclic = true
			break
		}
		nx, ok := r.waits[w]
		if !ok || nx.finished() {
			break
		}
		labels = append(labels, nx.label)
		w = nx.waiter()
	}
	if cyclic {
		delete(r.waits, cur)
		r.mu.Unlock()
		labels = append(labels, labels[0])
		return fmt.Errorf("%s: dependency cycle: %s", label, strings.Join(labels, " -> "))
	}
	r.mu.Unlock()
	defer r.unwait(cur)
	return st.wait()
}

// unwait drops cur's registered wait edge after its wait resolves.
func (r *Runner) unwait(cur *runtime.Task) {
	r.mu.Lock()
	delete(r.waits, cur)
	r.mu.Unlock()
}

// NewRunner creates a runner whose working directory is dir — scripts and
// subprocesses alike see it as their cwd (the engine's virtual cwd plus
// cmd.Dir on every spawned command).
func NewRunner(dir string, stdout, stderr io.Writer) *Runner {
	ioMu := &sync.Mutex{}
	r := &Runner{
		stdout:    syncWriter{mu: ioMu, w: stdout},
		stderr:    syncWriter{mu: ioMu, w: stderr},
		depStates: map[string]*depState{},
		waits:     map[*runtime.Task]*depState{},
	}
	e := minigo.NewEngine(dir,
		minigo.WithWorkingDir(dir),
		minigo.WithOutput(r.stdout),
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
		return nil, taskfileErr(file, err)
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
		return taskfileErr(file, err)
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
		"SerialDeps": &runtime.BuiltinFunc{Name: "task.SerialDeps", Fn: r.serialDeps},
		"F": &runtime.BuiltinFunc{Name: "task.F", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) == 0 {
				return nil, errors.New("task.F needs a task function")
			}
			return &runtime.GoValue{V: taskCall{fn: args[0], args: args[1:]}}, nil
		}},
		"Sh": &runtime.BuiltinFunc{Name: "task.Sh", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, argErr("task.Sh takes 1 arg, got %d", len(args))
			}
			cmdline, err := strArg("task.Sh", args, 0)
			if err != nil {
				return nil, err
			}
			cmd := exec.Command("sh", "-c", cmdline)
			cmd.Dir = r.engine.WorkingDir()
			cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
			return errOf(exitErr(cmd.Run(), fmt.Sprintf("sh -c %q", cmdline)))
		}},
		"Run": &runtime.BuiltinFunc{Name: "task.Run", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			return errOf(r.runCmd("", args, 0, "task.Run"))
		}},
		"RunIn": &runtime.BuiltinFunc{Name: "task.RunIn", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			dir, err := strArg("task.RunIn", args, 0)
			if err != nil {
				return nil, err
			}
			return errOf(r.runCmd(dir, args, 1, "task.RunIn"))
		}},
		"Output": &runtime.BuiltinFunc{Name: "task.Output", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			name, err := strArg("task.Output", args, 0)
			if err != nil {
				return nil, err
			}
			argv, err := strArgs("task.Output", args[1:], 2)
			if err != nil {
				return nil, err
			}
			cmd := exec.Command(name, argv...)
			cmd.Dir = r.engine.WorkingDir()
			cmd.Stderr = r.stderr
			out, err := cmd.Output()
			ev, _ := errOf(exitErr(err, cmdLabel("", name, argv)))
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
			if len(args) != 1 {
				return nil, argErr("task.Env takes 1 arg, got %d", len(args))
			}
			name, err := strArg("task.Env", args, 0)
			if err != nil {
				return nil, err
			}
			return os.Getenv(name), nil
		}},
	}
}

// deps implements task.Deps: every dep is claimed once per runner
// invocation and run on a spawned goroutine — truly in parallel. A dep
// that fails (call error or error result) fails the whole process like
// a Go panic; dedup and cycle detection share depStates with SerialDeps
// so mixed-mode graphs still converge on one claim per dep.
func (r *Runner) deps(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	cur := v.Task()
	states := make([]*depState, 0, len(args))
	for _, a := range args {
		fn, fargs, key, label := depOf(a)
		if key == "" {
			return nil, fmt.Errorf("task.Deps: cannot use %T as a dependency", a)
		}
		st, mine := r.claimDep(key, cur, label, func() *runtime.Task {
			// a dep that returns an error becomes a call error so the
			// process fails fast, like a panic in real Go
			return v.Spawn(&runtime.BuiltinFunc{Name: "dep:" + label, Fn: func(v runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
				res, err := v.Call(fn, fargs)
				if err != nil {
					return nil, err
				}
				if rerr := resultErr(res); rerr != nil {
					return nil, rerr
				}
				return res, nil
			}}, nil)
		})
		if !mine {
			if w := st.waiter(); w == cur || taskInAncestry(w, cur) {
				return nil, fmt.Errorf("task.Deps: dependency cycle at %s", label)
			}
		}
		states = append(states, st)
	}
	for _, st := range states {
		if err := r.await(cur, st, "task.Deps"); err != nil {
			return nil, fmt.Errorf("dep %s: %w", st.label, err)
		}
	}
	return runtime.NIL, nil
}

// serialDeps implements task.SerialDeps: claims still dedup/cycle-check
// through depStates, but the claimed dep runs inline on the calling
// goroutine — ordered execution for scripts that want it.
func (r *Runner) serialDeps(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	cur := v.Task()
	for _, a := range args {
		fn, fargs, key, label := depOf(a)
		if key == "" {
			return nil, fmt.Errorf("task.SerialDeps: cannot use %T as a dependency", a)
		}
		st, mine := r.claimDep(key, cur, label, nil)
		if !mine {
			if w := st.waiter(); w == cur || taskInAncestry(w, cur) {
				return nil, fmt.Errorf("task.SerialDeps: dependency cycle at %s", label)
			}
			if err := r.await(cur, st, "task.SerialDeps"); err != nil {
				return nil, fmt.Errorf("dep %s: %w", label, err)
			}
			continue
		}
		res, err := v.Call(fn, fargs)
		if err == nil {
			err = resultErr(res)
		}
		st.err = err
		close(st.done)
		if err != nil {
			return nil, fmt.Errorf("dep %s: %w", label, err)
		}
	}
	return runtime.NIL, nil
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
// uses the engine's virtual cwd. namePos is the index of the program
// name in args (Run: 0; RunIn: 1, after the dir arg).
func (r *Runner) runCmd(dir string, args []runtime.Value, namePos int, label string) error {
	name, err := strArg(label, args, namePos)
	if err != nil {
		return err
	}
	argv, err := strArgs(label, args[namePos+1:], namePos+2)
	if err != nil {
		return err
	}
	cmd := exec.Command(name, argv...)
	if dir == "" {
		cmd.Dir = r.engine.WorkingDir()
	} else if filepath.IsAbs(dir) {
		cmd.Dir = dir
	} else {
		cmd.Dir = filepath.Join(r.engine.WorkingDir(), dir)
	}
	cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
	return exitErr(cmd.Run(), cmdLabel(dir, name, argv))
}

// exitErr rewrites a bare "exit status N" into "cmd ...: exit status N" —
// *exec.ExitError does not name the command that failed, so without this
// a failing dep reports only a number. Startup failures (missing binary,
// bad dir, permissions) already name their target and pass through.
func exitErr(err error, label string) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Errorf("%s: %w", label, err)
	}
	return err
}

// cmdLabel renders the spawned command in the script's own vocabulary:
// "prog arg1 arg2", or "(in dir) prog arg1 arg2" for task.RunIn.
func cmdLabel(dir, name string, args []string) string {
	s := strings.Join(append([]string{name}, args...), " ")
	if dir != "" {
		return "(in " + dir + ") " + s
	}
	return s
}

// target implements task.Target: target is up to date when it exists and
// is newer than (or equal to) every dep file's mtime.
func (r *Runner) target(args []runtime.Value) (runtime.Value, error) {
	tp, err := strArg("task.Target", args, 0)
	if err != nil {
		return nil, err
	}
	deps, err := strArgs("task.Target", args[1:], 2)
	if err != nil {
		return nil, err
	}
	t := r.resolvePath(tp)
	st, err := os.Stat(t)
	if err != nil {
		if os.IsNotExist(err) {
			return &runtime.Tuple{Elems: []runtime.Value{false, runtime.NIL}}, nil
		}
		return &runtime.Tuple{Elems: []runtime.Value{false, &runtime.GoValue{V: err}}}, nil
	}
	mtime := st.ModTime()
	for _, dp := range deps {
		p := r.resolvePath(dp)
		ds, err := os.Stat(p)
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

// strArg returns the string the script passed at position i. The stub
// signatures declare `string`, so anything else is the same bug `go
// build` would reject — silently stringifying it misnames the failure
// (task.Run(42) would report "exec: \"42\": file not found", blaming
// PATH instead of the call site).
func strArg(label string, args []runtime.Value, i int) (string, error) {
	if i >= len(args) {
		return "", argErr("%s needs arg %d", label, i+1)
	}
	s, ok := strVal(args[i])
	if !ok {
		return "", argErr("%s arg %d must be a string, got %s", label, i+1, kindOf(args[i]))
	}
	return s, nil
}

// strArgs converts the argument tail starting at 1-based position `from`
// (the position args[0] occupies in the full call — 2 after a name arg,
// 3 for RunIn's dir+name).
func strArgs(label string, args []runtime.Value, from int) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		s, ok := strVal(a)
		if !ok {
			return nil, argErr("%s arg %d must be a string, got %s", label, from+i, kindOf(a))
		}
		out[i] = s
	}
	return out, nil
}

// strVal reads a script value as a string, honoring the declared `string`
// contract: a Named tag unwraps to the underlying value first.
func strVal(v runtime.Value) (string, bool) {
	if n, ok := v.(*runtime.Named); ok {
		v = n.V
	}
	s, ok := v.(string)
	return s, ok
}

// kindOf names a script value's type the way the Taskfile author spells
// it — "int", "func A", "Seconds" — not the runtime struct name.
func kindOf(v runtime.Value) string {
	switch x := v.(type) {
	case nil, runtime.Nil:
		return "nil"
	case *runtime.Named:
		if x.Typ != nil {
			return x.Typ.Name
		}
		return kindOf(x.V)
	case int64:
		return "int"
	case float64:
		return "float64"
	case bool:
		return "bool"
	case string:
		return "string"
	case *runtime.Function:
		return "func " + x.Name
	case *runtime.Closure:
		return "func literal"
	case *runtime.BuiltinFunc:
		return "builtin " + x.Name
	case *runtime.BoundMethod:
		return "bound method " + x.Fn.Name
	default:
		return fmt.Sprintf("%T", v)
	}
}

// loadError wraps a LoadFile failure so the CLI can attribute it to the
// Taskfile or the -f flag instead of prefixing it with the task name.
type loadError struct{ err error }

func (e *loadError) Error() string { return e.err.Error() }
func (e *loadError) Unwrap() error { return e.err }

// taskfileErr classifies a LoadFile failure by who has to fix it: a
// missing path or a directory is a bad -f argument; anything else is the
// Taskfile's contents (parse errors, undeclared names, unsupported
// syntax).
func taskfileErr(file string, err error) error {
	if st, statErr := os.Stat(file); statErr == nil && st.IsDir() {
		return &loadError{fmt.Errorf("taskfile %s is a directory. Fix the -f argument", file)}
	}
	if errors.Is(err, os.ErrNotExist) {
		return &loadError{fmt.Errorf("taskfile %s does not exist. Fix the -f argument", file)}
	}
	return &loadError{fmt.Errorf("%s. Fix the Taskfile", err)}
}

// argError marks a call-contract violation (wrong arg type or count).
// errOf escalates it to a call error: the script called the function
// wrong — like `task.Run(42)`, which `go build` would reject — so it
// must surface even from a `func()` task that ignores the error result.
type argError struct{ err error }

func (e *argError) Error() string { return e.err.Error() }
func (e *argError) Unwrap() error { return e.err }

func argErr(format string, args ...any) error {
	return &argError{err: fmt.Errorf(format, args...)}
}

// errOf marshals a host error to the script-visible (err) convention.
// Contract violations (argError) are not marshaled: they escalate to a
// call error instead of an ignorable error return value.
func errOf(err error) (runtime.Value, error) {
	var ae *argError
	if errors.As(err, &ae) {
		return nil, ae
	}
	if err != nil {
		return &runtime.GoValue{V: err}, nil
	}
	return runtime.NIL, nil
}
