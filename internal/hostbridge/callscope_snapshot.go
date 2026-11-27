package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// scriptSnap records a script map's key->value set.
func scriptSnap(s *runtime.Map) map[string]runtime.Value {
	out := make(map[string]runtime.Value, s.Len())
	for i := 0; i < s.Len(); i++ {
		k, v := s.At(i)
		out[projKey(k)] = v
	}
	return out
}

// hostSnap records a host map's key set.
func hostSnap(rv reflect.Value) map[string]bool {
	out := make(map[string]bool, rv.Len())
	it := rv.MapRange()
	for it.Next() {
		out[hostKey(it.Key())] = true
	}
	return out
}

// sameSlot reports whether the script value at a key reads unchanged
// since the snapshot — a scalar whose payload is equal — versus a
// write that replaced it. Containers always report false: their
// writes land inside, which the per-child merge handles.
func sameSlot(cur, old runtime.Value) bool {
	pa, pb := scalarPayload(cur), scalarPayload(old)
	if pa == nil || pb == nil {
		return false
	}
	switch runtime.Unwrap(pa).(type) {
	case *runtime.Map, *runtime.Slice, *runtime.Struct, *runtime.Cell:
		// writes land inside the pointee — the per-child merge
		// decides whether the entry still matches its projection.
		return false
	}
	return pa == pb
}
