// select.go resolves and drives channel operations for the interpreter:
// the machinery itself lives in internal/hostbridge (chanops.go); here
// the frame-side capabilities it needs get bundled per call.
package vm

import (
	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// chanHooks bundles the VM operations the hostbridge channel ops need:
// trap position, park bookkeeping, the process-done select arm, element
// zero values and typedef resolution, and managed-channel liveness.
func (v *VM) chanHooks(f *frame) *hostbridge.ChanHooks {
	return &hostbridge.ChanHooks{
		VC:   v,
		Trap: f.trap,
		Park: v.park,
		Done: v.doneChan,
		ZeroValue: func(et *runtime.TypeDef) runtime.Value {
			return v.zeroValue(f, et)
		},
		ElemTypedef: func(td *runtime.TypeDef) *runtime.TypeDef {
			return v.elemTypedef(f, td)
		},
		Managed: func(ptr uintptr) bool {
			if p := v.proc.Load(); p != nil {
				managed, _ := p.ChanState(ptr)
				return managed
			}
			return false
		},
		Exit: func() any { return procExit{} },
	}
}
