package hostbridge

import "github.com/podhmo/minigo/runtime"

// projectionInterfaceKey converts a script key to the host value it has
// inside an interface-keyed projection. Scalars keep their Go type:
// a script int is host int, while sized integers keep their width. Only
// values with no host scalar equivalent fall back to runtime.UnwrapKey's
// comparable box.
func projectionInterfaceKey(v runtime.Value) any {
	peeled := map[runtime.Value]struct{}{}
	for {
		switch x := v.(type) {
		case *runtime.Named:
			if x.Typ != nil {
				if x.Typ.HostScalar != nil {
					if hv, ok := runtime.HostScalarOf(x.Typ, x.V); ok {
						return hv
					}
				}
				if rt := HostBasicTypes[runtime.BasicNameOf(x.Typ)]; rt != nil && x.Typ.HostNew == nil {
					if rv, ok := convertBasic(x.V, rt); ok {
						return rv.Interface()
					}
				}
			}
			v = x.V
			continue
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			if _, dup := peeled[v]; dup {
				return v
			}
			peeled[v] = struct{}{}
			d, ok := runtime.Deref(v)
			if !ok {
				return v
			}
			v = d
		case *runtime.GoValue:
			return x.V
		case *runtime.UConst:
			nv, err := runtime.UConstNative(x)
			if err != nil {
				return x
			}
			v = nv
		default:
			switch v.(type) {
			case nil, runtime.Nil, *runtime.IfaceNil, *runtime.TypedNil:
				return nil
			}
			if i, ok := v.(int64); ok {
				return int(i)
			}
			return v
		}
	}
}
