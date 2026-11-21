package minigo

import (
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// materializerOf views a builtin caller as the optional constant
// materialization capability display needs.
func materializerOf(v runtime.VMCaller) runtime.ConstMaterializer {
	mc, _ := v.(runtime.ConstMaterializer)
	return mc
}

// Format returns a form of v to print with %v, such as an [Engine.Run]
// result, without running script code. Unlike repl.REPL.Display it does
// not call String or Error methods, and nil values take the print
// builtin's spelling: (0x0,0x0) for a nil interface, [0/0]0x0 for a nil
// slice.
func Format(v runtime.Value) any {
	return hostbridge.Display(nil, v)
}
