package mtesting

import (
	"fmt"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// Lookup returns the flag's storage cell (an approximation — the real
// Lookup returns a *Flag descriptor).
func (fs *flagState) lookup(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
	if len(args) == 0 {
		return runtime.NIL, nil
	}
	d, ok := fs.defs[stdlib.Str(args[0])]
	if !ok {
		return runtime.NIL, nil
	}
	return d.value, nil
}

func (fs *flagState) printDefaults(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
	if fs.suite == nil || fs.suite.Emit == nil {
		return runtime.NIL, nil
	}
	for _, d := range fs.order {
		fs.suite.Emit(fmt.Sprintf("  -%s\n    \t%s", d.name, d.usage))
	}
	return runtime.NIL, nil
}
