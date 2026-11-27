package runtime

import "errors"

// DrainDefersError is a host control-flow error that, when represented
// as a [Trap], must drain the current frame's remaining deferred calls
// before it reaches the host. testing's FailNow/SkipNow use it for
// runtime.Goexit-like test termination.
type DrainDefersError interface {
	error
	DrainDefers()
}

// TrapDrainsDefers reports whether tr wraps a [DrainDefersError].
func TrapDrainsDefers(tr *Trap) bool {
	if tr == nil {
		return false
	}
	var d DrainDefersError
	return errors.As(tr, &d)
}
