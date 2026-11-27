package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// lazyMethod binds a projected method as a niladic host func — the
// merge-time projection's stand-in for the eager call. Template's
// evalField invokes func fields, so the member still reads its
// computed value; the call just runs at read time, outside the merge,
// where its own host calls no longer feed the re-projection loop
// (issue #1399). A failed call reads nil, printing <no value> like the
// eager path's dropped entry.
func (h *HostMarshal) lazyMethod(bm *runtime.BoundMethod, vc runtime.VMCaller, nout int) reflect.Value {
	return reflect.MakeFunc(reflect.TypeFor[func() any](), func([]reflect.Value) []reflect.Value {
		out := reflect.Zero(anyType)
		res, err := vc.Call(bm, nil)
		h.M.noteScriptCallback()
		if err != nil {
			return []reflect.Value{out}
		}
		if nout == 2 {
			t, ok := res.(*runtime.Tuple)
			if !ok || len(t.Elems) != 2 || !nilishHost(t.Elems[1]) {
				return []reflect.Value{out}
			}
			res = t.Elems[0]
		}
		if hv := h.M.DeepHost(res, vc, h.M.NewWalk()); hv != nil {
			out = reflect.ValueOf(hv)
		}
		return []reflect.Value{out}
	})
}
