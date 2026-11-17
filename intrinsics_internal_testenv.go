package minigo

import (
	"os"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installInternalTestenv(h *hostHelpers) {
	// internal/testenv exists only for GOROOT's own tests; the one entry
	// the upstream suites reach for is SetGODEBUG. Upstream calls
	// t.Helper() + t.Setenv (which registers a cleanup restoring the
	// env) — writing the host env directly gives the same observable
	// result for a test process, and the bound godebug Setting reads
	// that same host env, so the knob actually flips. There is no
	// testing.TB to satisfy: the t argument is ignored.
	e.Bind("internal/testenv", map[string]runtime.Value{
		"SetGODEBUG": h.fn2("testenv.SetGODEBUG", func(a []any) (any, error) {
			return nil, os.Setenv("GODEBUG", os.Getenv("GODEBUG")+","+str(a[1]))
		}),
	})
}
