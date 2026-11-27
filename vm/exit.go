package vm

import "fmt"

// procExit is the unwind raised in a parked goroutine when its process
// ends (root Call returned, or a sibling goroutine's panic failed the
// process). It is deliberately not a *runtime.Panic: defers still run
// but recover() cannot see it, and it is swallowed at the spawn
// boundary — the Go analogue is process exit, where other goroutines
// die without a catchable panic.
type procExit struct{}

func (procExit) Error() string { return "minigo: process exited" }

// ExitRequest is the error for a script's os.Exit(code): like a real
// process exit it skips every pending defer, cannot be recovered by the
// script, and stops the process's other goroutines. [VM.Call] returns it
// for a nonzero code and a nil error for code 0.
type ExitRequest struct{ Code int }

func (e *ExitRequest) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// IsProcExit reports whether err is the error a goroutine gets when its
// process ends under it (the root Call returned, or a sibling goroutine
// failed the process). Host code collecting [runtime.Task] errors can skip
// these to find the real failure.
func IsProcExit(err error) bool { _, ok := err.(procExit); return ok }
