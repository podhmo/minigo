package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// fillMethodSlot evaluates one method slot. Refresh first clears the
// slot, so a failed re-evaluation cannot leave the previous result behind.
func fillMethodSlot(fv reflect.Value, initial bool, m *runtime.Function, fill func(reflect.Value)) {
	if !initial && !refreshableMethod(m) {
		return
	}
	if !initial && fv.CanSet() {
		fv.SetZero()
	}
	fill(fv)
}
