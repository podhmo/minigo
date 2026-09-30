// Package task is the script-side API for task-run Taskfiles.
//
// Taskfiles import this package and call its functions; every body panics
// so real Go tooling (gopls, go vet, go build) sees valid Go, while the
// task-run engine binds the import path to intrinsics at run time — the
// same trick minigo.dev/host uses for the minigo interpreter.
//
// A Taskfile is a Go source file (usually with a `//go:build task` tag so
// `go build ./...` skips it). Any exported function of the shape
// `func()`, `func() error`, or `func(<string params>...) (error)` is a
// task; the doc comment becomes its `-l` description.
package task

// Deps runs each dependency task once, in order. Arguments are task
// functions (`func()` or `func() error`) or wrapped calls from F.
// Dependencies already executed are skipped; a dependency cycle traps.
func Deps(deps ...any) { panic("minigo intrinsic") }

// SerialDeps is Deps with serial intent documented — deps already run
// sequentially under minigo (parallel deps need real goroutines; see
// sketch/plan-task-runner.md).
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
