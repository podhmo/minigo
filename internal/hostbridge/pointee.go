package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// HostPointeeEql compares two host pointee boxes' contents like gc's
// `r == *p` compares the pointees — the payload pointers differ after
// a snapshot while the pointees are equal. ok is false when either
// payload is not a pointee box (the caller falls back to the plain
// payload compare).
func HostPointeeEql(a, b runtime.Value) (eq, ok bool) {
	arv, aok := hostPointeeOf(a)
	brv, bok := hostPointeeOf(b)
	if !aok || !bok {
		return false, false
	}
	if arv.Type() != brv.Type() {
		return false, true
	}
	if !arv.Type().Comparable() {
		panic(runtime.ComparingUncomparablePanic(arv.Type().String()))
	}
	return arv.Interface() == brv.Interface(), true
}

// hostPointeeOf reads the pointee a host value box holds: a *T box
// gives its Elem, any other host box the value itself.
func hostPointeeOf(v runtime.Value) (reflect.Value, bool) {
	gv, ok := v.(*runtime.GoValue)
	if !ok {
		return reflect.Value{}, false
	}
	rv := reflect.ValueOf(gv.V)
	if !rv.IsValid() {
		return reflect.Value{}, false
	}
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return reflect.Value{}, false
		}
		rv = rv.Elem()
	}
	return rv, true
}
