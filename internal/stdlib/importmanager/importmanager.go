// Package importmanager installs the importmanager intrinsic: scripts
// generating Go code manage import aliases through the same
// [github.com/podhmo/minigo/pkg/importmanager] the host generators use.
package importmanager

import (
	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/pkg/importmanager"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers) {
	pkg := map[string]runtime.Value{
		// New returns the host *importmanager.Manager; scripts call its
		// methods (Add, Qualify, Imports) through the host member lookup.
		"New": h.Fn("importmanager.New", func(a []any) (any, error) {
			return importmanager.New(stdlib.Str(a[0])), nil
		}),
		// the type itself, so `importmanager.Manager` names a receiver
		// for method expressions and declarations.
		"Manager": stdlib.HostType("github.com/podhmo/minigo/pkg/importmanager.Manager", func() any {
			return &importmanager.Manager{}
		}),
	}
	for _, path := range []string{"minigo.dev/importmanager", "github.com/podhmo/minigo/pkg/importmanager"} {
		bind(path, pkg)
	}
}
