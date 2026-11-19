package testenv

import (
	"os"

	"github.com/podhmo/minigo/internal/stdlib"

	"github.com/podhmo/minigo/runtime"
)

// Install binds the internal/testenv stub surface (pure helpers GOROOT sources call).
func Install(bind stdlib.Bind, h stdlib.Helpers) {
	// internal/testenv exists only for GOROOT's own tests; the one entry
	// the upstream suites reach for is SetGODEBUG. Upstream calls
	// t.Helper() + t.Setenv (which registers a cleanup restoring the
	// env) — writing the host env directly gives the same observable
	// result for a test process, and the bound godebug Setting reads
	// that same host env, so the knob actually flips. There is no
	// testing.TB to satisfy: the t argument is ignored.
	bind("internal/testenv", map[string]runtime.Value{
		"SetGODEBUG": h.Fn2("testenv.SetGODEBUG", func(a []any) (any, error) {
			return nil, os.Setenv("GODEBUG", os.Getenv("GODEBUG")+","+stdlib.Str(a[1]))
		}),
	})
}
