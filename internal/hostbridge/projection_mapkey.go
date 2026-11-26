package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// projectionMapKey converts a script key with the same canonical form
// the initial projection used. Interface keys in particular must keep
// runtime.UnwrapKey's representation: DeepHost converts a script int to
// host int, while the projected map already holds the canonical int64.
func (m *Marshal) projectionMapKey(sk runtime.Value, krt reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	if krt.Kind() == reflect.Interface {
		hk := reflect.New(krt).Elem()
		if uk := runtime.UnwrapKey(sk); uk != nil {
			hk.Set(reflect.ValueOf(uk))
		}
		return hk, nil
	}
	return m.ToReflect(sk, krt, vc)
}
