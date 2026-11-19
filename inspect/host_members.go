package inspect

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// hostMethodSpecs enumerates the reflect method set of a host-bound
// embedded decl — the readable remainder of MethodSetOf when a bound
// type's AST lives nowhere minigo can reach. ptr selects the pointer
// method set, matching the value/pointer promotion rule source decls
// follow. The returned methods have no Decl (no source decl stands
// behind them); callers set Via to the promotion origin.
func hostMethodSpecs(d *Decl, ptr bool) []*Method {
	var rt reflect.Type
	if d.htd != nil && d.htd.HostNew != nil {
		rt = d.htd.HostType()
	}
	if rt == nil {
		// a decl built without a TypeDef binding still carries its
		// Go type on htype.
		rt = d.htype
	}
	if rt == nil {
		return nil
	}
	if ptr && rt.Kind() != reflect.Ptr {
		rt = reflect.PointerTo(rt)
	} else if !ptr && rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	n := rt.NumMethod()
	out := make([]*Method, 0, n)
	for i := 0; i < n; i++ {
		m := rt.Method(i)
		mt := m.Type
		out = append(out, &Method{
			Name: m.Name,
			Sig: &Sig{
				Recv:    &Field{Type: NewHostType(mt.In(0))},
				Params:  reflectFields(mt.NumIn()-1, func(j int) reflect.Type { return mt.In(j + 1) }),
				Results: reflectFields(mt.NumOut(), mt.Out),
			},
		})
	}
	return out
}

// reflectFields boxes reflect parameter/result types as unnamed fields.
func reflectFields(n int, at func(i int) reflect.Type) *runtime.Slice {
	xs := make([]runtime.Value, n)
	for i := 0; i < n; i++ {
		xs[i] = &runtime.GoValue{V: &Field{Type: NewHostType(at(i))}}
	}
	return &runtime.Slice{Elems: xs}
}
