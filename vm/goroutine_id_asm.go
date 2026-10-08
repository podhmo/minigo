//go:build arm64 || amd64

package vm

// goroutineID reports an identity for the calling goroutine: the address
// of its runtime g. Call only compares ids — a same-goroutine re-entry
// against a foreign one (a host-retained callback firing on another
// goroutine) — and every id it holds belongs to a goroutine that is
// still running (the Call owner, a helper inside a blocking host call),
// so a g recycled after its goroutine exits never collides. The stdlib
// exposes no accessor; runtime.Stack's header (goroutine_id.go) walks
// the whole interpreter stack per call.
func goroutineID() int64 { return int64(getg()) }

// getg returns the current g pointer (goroutine_id_*.s).
func getg() uintptr
