package race

import (
	"github.com/podhmo/minigo/internal/stdlib"

	"github.com/podhmo/minigo/runtime"
)

// Install binds the internal/race stub surface (pure helpers GOROOT sources call).
func Install(bind stdlib.Bind, h stdlib.Helpers) {
	bind("internal/race", map[string]runtime.Value{
		// the interpreter runs no race detector: iter.Pull's
		// race.Acquire/Release/Write calls are no-ops — the coroutine
		// rendezvous itself carries the synchronization.
		"Acquire":      h.Fn("internal/race.Acquire", func(a []any) (any, error) { return nil, nil }),
		"Release":      h.Fn("internal/race.Release", func(a []any) (any, error) { return nil, nil }),
		"ReleaseMerge": h.Fn("internal/race.ReleaseMerge", func(a []any) (any, error) { return nil, nil }),
		"Read":         h.Fn("internal/race.Read", func(a []any) (any, error) { return nil, nil }),
		"Write":        h.Fn("internal/race.Write", func(a []any) (any, error) { return nil, nil }),
		"Disable":      h.Fn("internal/race.Disable", func(a []any) (any, error) { return nil, nil }),
		"Enable":       h.Fn("internal/race.Enable", func(a []any) (any, error) { return nil, nil }),
	})
}
