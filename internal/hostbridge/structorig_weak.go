package hostbridge

import (
	"weak"

	"github.com/podhmo/minigo/runtime"
)

// weakStructOrig is the registry-side reference to a projection's script
// original. The process-wide table must not own that original, or it
// becomes the root of the session/projection cycle it exists to describe;
// the host call's identity walk owns the strong round-trip lifetime.
type structOrigToken struct{ _ byte }

// structOrigEntry pairs the weak original with a registration token:
// a cleanup for an old projection must not delete the entry minted when a
// later projection reuses its address before the old cleanup runs.
type structOrigEntry struct {
	orig  weakStructOrig
	token *structOrigToken
}

type weakStructOrig struct {
	st    weak.Pointer[runtime.Struct]
	cell  weak.Pointer[runtime.Cell]
	field weak.Pointer[runtime.FieldRef]
	index weak.Pointer[runtime.IndexRef]
	deref weak.Pointer[runtime.DerefRef]
}

func makeWeakStructOrig(v runtime.Value) (weakStructOrig, bool) {
	switch x := v.(type) {
	case *runtime.Struct:
		return weakStructOrig{st: weak.Make(x)}, true
	case *runtime.Cell:
		return weakStructOrig{cell: weak.Make(x)}, true
	case *runtime.FieldRef:
		return weakStructOrig{field: weak.Make(x)}, true
	case *runtime.IndexRef:
		return weakStructOrig{index: weak.Make(x)}, true
	case *runtime.DerefRef:
		return weakStructOrig{deref: weak.Make(x)}, true
	}
	return weakStructOrig{}, false
}

func (w weakStructOrig) Value() runtime.Value {
	if v := w.st.Value(); v != nil {
		return v
	}
	if v := w.cell.Value(); v != nil {
		return v
	}
	if v := w.field.Value(); v != nil {
		return v
	}
	if v := w.index.Value(); v != nil {
		return v
	}
	if v := w.deref.Value(); v != nil {
		return v
	}
	return nil
}
