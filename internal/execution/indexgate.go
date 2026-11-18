// Package execution holds the runtime's loading and initialization
// lifecycle machinery, extracted out of runtime so it can grow without
// dragging the value model along (P2-14).
package execution

// IndexGate publishes a package's index completion: the loader calls
// Mark when indexing starts and Finish however it ends (success or
// failure), and readers on other goroutines Wait instead of seeing a
// half-indexed package. A gate never marked reports Ready — bound host
// packages index at construction.
type IndexGate struct {
	done chan struct{}
}

// Mark declares that indexing is in progress: Wait blocks until Finish.
// Loaders call it when they create the package, before publishing it.
func (g *IndexGate) Mark() {
	if g.done == nil {
		g.done = make(chan struct{})
	}
}

// Finish releases the readers held since Mark, whether indexing
// succeeded or failed.
func (g *IndexGate) Finish() {
	if g.done != nil {
		close(g.done)
	}
}

// Wait blocks until the gate is finished (see Mark); afterwards the
// package's index and import scopes are safe to read from any goroutine.
func (g *IndexGate) Wait() {
	if g.done != nil {
		<-g.done
	}
}

// Ready reports whether indexing finished — the non-blocking form of
// Wait. A package never marked is ready.
func (g *IndexGate) Ready() bool {
	if g.done == nil {
		return true
	}
	select {
	case <-g.done:
		return true
	default:
		return false
	}
}
