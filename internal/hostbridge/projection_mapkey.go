package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// projectionMapKey converts a script key with the same form the initial
// projection used.
func (m *Marshal) projectionMapKey(sk runtime.Value, krt reflect.Type, vc runtime.VMCaller) (reflect.Value, error) {
	if krt.Kind() == reflect.Interface {
		hk := reflect.New(krt).Elem()
		if uk := projectionInterfaceKey(sk); uk != nil {
			hk.Set(reflect.ValueOf(uk))
		}
		return hk, nil
	}
	return m.ToReflect(sk, krt, vc)
}
