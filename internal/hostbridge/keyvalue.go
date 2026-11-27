package hostbridge

import (
	"math"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// goSlotKeyValueOf is GoSlotValueOf for a host map key. Interface-keyed
// projections store script ints as host int, so an int64 or uint64 key
// there is the host's sized integer type, not a script int payload.
func goSlotKeyValueOf(rv reflect.Value) runtime.Value {
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		e := rv.Elem()
		switch e.Kind() {
		case reflect.Int64:
			return runtime.Tag(sizedIntTyp(reflect.Int64), e.Int())
		case reflect.Uint64:
			if u := e.Uint(); u <= math.MaxInt64 {
				return runtime.Tag(sizedIntTyp(reflect.Uint64), int64(u))
			}
		}
	}
	return GoSlotValueOf(rv)
}
