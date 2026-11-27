package hostbridge

import (
	"fmt"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// projKey renders a script map key for the merge snapshots. Any
// deterministic form works — the repr compares a key only to its own
// earlier self.
func projKey(k runtime.Value) string {
	key := runtime.IfaceKeyOf(k)
	rv := reflect.ValueOf(key)
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		return fmt.Sprintf("%T@%x", key, rv.Pointer())
	}
	return fmt.Sprintf("%T:%#v", key, key)
}

// hostKey is projKey for the host side of a projection.
func hostKey(rv reflect.Value) string {
	for rv.Kind() == reflect.Interface && !rv.IsNil() {
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return "<nil>"
	}
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		return fmt.Sprintf("%T@%x", rv.Interface(), rv.Pointer())
	}
	v := rv.Interface()
	return fmt.Sprintf("%T:%#v", v, v)
}
