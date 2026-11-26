// Package identity exposes host-side identity for conversion tests.
package identity

import "reflect"

// Same reports whether two interface arguments carry the same host map,
// slice, pointer, channel, function, or unsafe pointer. The funcmap test
// passes script maps; in that case the comparison is the host projection
// pointer, which is exactly what per-call conversion identity must share.
func Same(a, b any) bool {
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if !av.IsValid() || !bv.IsValid() || av.Type() != bv.Type() {
		return false
	}
	switch av.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return av.Pointer() == bv.Pointer()
	}
	return false
}

// AddIntKey writes through the host spelling of a script integer key.
func AddIntKey(x any) { x.(map[any]string)[int(1)] = "host" }

// Len reports the host projection's length, including key spellings the
// script side canonicalizes away.
func Len(m map[any]string) int { return len(m) }
