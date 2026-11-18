package execution

import (
	"fmt"
	"sync"
)

// Init runs a package's initializer at most once and caches its
// failure. It is type-free so runtime can own the state marking around
// it: the package decides what "failed" means through the fail
// callback, while Init keeps the once semantics — including the panic
// case, where sync.Once consumes the run whether or not an error is
// recorded.
type Init struct {
	once sync.Once
	err  error
}

// Run executes fn when it has not run before, and returns the cached
// error of the single run. fail (may be nil) observes every recorded
// error — a returned error and a panic alike — so the caller can mark
// its own failed state. A panic inside fn is converted to the cached
// error and then re-panicked: the once consumes the run either way,
// and recording first keeps later callers from seeing a
// successful-but-partial initialization.
func (i *Init) Run(fn func() error, fail func(error)) error {
	i.once.Do(func() {
		defer func() {
			if v := recover(); v != nil {
				i.err = asError(v)
				if fail != nil {
					fail(i.err)
				}
				panic(v)
			}
		}()
		if err := fn(); err != nil {
			i.err = err
			if fail != nil {
				fail(err)
			}
		}
	})
	return i.err
}

// Try is Run without failure marking or re-panicking: a panic inside fn
// is converted to the cached error and swallowed, so callers can fall
// back to a fuller initialization afterwards.
func (i *Init) Try(fn func() error) error {
	i.once.Do(func() {
		defer func() {
			if v := recover(); v != nil {
				i.err = asError(v)
			}
		}()
		i.err = fn()
	})
	return i.err
}

func asError(v any) error {
	if e, ok := v.(error); ok {
		return e
	}
	return fmt.Errorf("panic: %v", v)
}
