// Package task provides commands and dependencies for task-run Taskfiles.
//
// Taskfiles import this package and call [Run], [Sh] or [Deps]; [F] supplies
// arguments to dependencies. These functions require task-run's interpreter;
// do not call them from compiled Go.
//
// Tasks are exported functions with string parameters and either no result
// or an error result. Their doc comments supply the task-run -l descriptions.
// A //go:build task constraint can exclude a Taskfile from normal builds.
package task

// Deps runs each dependency task once, in parallel. Arguments are task
// functions (`func()` or `func() error`) or wrapped calls from F.
// Dependencies already executed are skipped (dedup is shared with
// SerialDeps); a dependency cycle traps. A dep that returns an error
// fails the whole run, like a panic in Go.
func Deps(deps ...any) { panic("minigo intrinsic") }

// SerialDeps runs each dependency task once, in order on the calling
// goroutine — serial intent for task graphs that rely on ordering.
func SerialDeps(deps ...any) { panic("minigo intrinsic") }

// F wraps a task function and its arguments so it can be passed to Deps —
// the counterpart of mage's mg.F. The pair (function, args) is the dedup
// key: the same wrapped call listed twice runs once.
func F(fn any, args ...any) any { panic("minigo intrinsic") }

// Sh runs a shell command line through `sh -c` with the runner's stdout
// and stderr wired up; the working directory is the Taskfile's directory.
func Sh(cmd string) error { panic("minigo intrinsic") }

// Run executes a program directly, streaming its output.
func Run(name string, args ...string) error { panic("minigo intrinsic") }

// RunIn is Run with an explicit working directory (relative paths resolve
// against the Taskfile's directory).
func RunIn(dir, name string, args ...string) error { panic("minigo intrinsic") }

// Output executes a program and returns its stdout, trimmed of a trailing
// newline.
func Output(name string, args ...string) (string, error) { panic("minigo intrinsic") }

// Target reports whether target is up to date: it exists and is not older
// than any of the dependency files (mtime comparison, like make).
func Target(target string, deps ...string) (bool, error) { panic("minigo intrinsic") }

// Log writes a line to the runner's stderr stream.
func Log(args ...any) { panic("minigo intrinsic") }

// Env reads an environment variable — shorthand for os.Getenv.
func Env(name string) string { panic("minigo intrinsic") }
