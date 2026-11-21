package minigo

import (
	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installInternalRace(h *hostHelpers) {
	e.bindBound("internal/race", map[string]runtime.Value{
		// the interpreter runs no race detector: iter.Pull's
		// race.Acquire/Release/Write calls are no-ops — the coroutine
		// rendezvous itself carries the synchronization.
		"Acquire":      h.fn("internal/race.Acquire", func(a []any) (any, error) { return nil, nil }),
		"Release":      h.fn("internal/race.Release", func(a []any) (any, error) { return nil, nil }),
		"ReleaseMerge": h.fn("internal/race.ReleaseMerge", func(a []any) (any, error) { return nil, nil }),
		"Read":         h.fn("internal/race.Read", func(a []any) (any, error) { return nil, nil }),
		"Write":        h.fn("internal/race.Write", func(a []any) (any, error) { return nil, nil }),
		"Disable":      h.fn("internal/race.Disable", func(a []any) (any, error) { return nil, nil }),
		"Enable":       h.fn("internal/race.Enable", func(a []any) (any, error) { return nil, nil }),
	})
}
