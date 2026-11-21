package stdlib

import (
	"errors"
	"fmt"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// UnsafeElems resolves unsafe.String/Slice's (ptr, len) to the n
func UnsafeElems(p, nv runtime.Value) ([]runtime.Value, error) {
	n := int(Int64Of(hostbridge.GoNative(nv)))
	if p == nil || p == runtime.NIL {
		if n == 0 {
			return nil, nil
		}
		return nil, errors.New("unsafe: ptr is nil and len is not zero")
	}
	if tn, ok := runtime.Unwrap(p).(*runtime.TypedNil); ok {
		// a typed nil pointer follows the same nil rule — len 0 is a
		// legal no-op, anything else panics like an untyped nil.
		if tn.Typ != nil && tn.Typ.Kind == runtime.KindPointer {
			if n == 0 {
				return nil, nil
			}
			return nil, errors.New("unsafe: ptr is nil and len is not zero")
		}
		return nil, fmt.Errorf("unsafe: unsupported pointer %T (only element pointers)", p)
	}
	ref, ok := runtime.Unwrap(p).(*runtime.IndexRef)
	if !ok {
		return nil, fmt.Errorf("unsafe: unsupported pointer %T (only element pointers)", p)
	}
	sl := ref.Slice()
	i, _ := runtime.Unwrap(ref.Key).(int64)
	if sl == nil || int(i)+n > cap(sl.Elems) {
		return nil, errors.New("unsafe: pointer range out of bounds")
	}
	return sl.Elems[i : int(i)+n : int(i)+n], nil
}
