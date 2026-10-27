package runtime

import (
	"iter"
	"reflect"
)

// hostSeqKey keys hostSeqTypes: the element types of a Seq (b nil) or
// Seq2.
type hostSeqKey struct{ a, b reflect.Type }

// hostSeqTypes holds the host iter.Seq / iter.Seq2 instantiations over
// the predeclared basic types (Seq2 keyed by int or string) — reflect
// cannot instantiate a generic type at run time, so the common ones are
// compiled in.
var hostSeqTypes = func() map[hostSeqKey]reflect.Type {
	m := map[hostSeqKey]reflect.Type{}
	addSeqTypes[bool](m)
	addSeqTypes[string](m)
	addSeqTypes[int](m)
	addSeqTypes[int8](m)
	addSeqTypes[int16](m)
	addSeqTypes[int32](m)
	addSeqTypes[int64](m)
	addSeqTypes[uint](m)
	addSeqTypes[uint8](m)
	addSeqTypes[uint16](m)
	addSeqTypes[uint32](m)
	addSeqTypes[uint64](m)
	addSeqTypes[uintptr](m)
	addSeqTypes[float32](m)
	addSeqTypes[float64](m)
	addSeqTypes[complex64](m)
	addSeqTypes[complex128](m)
	return m
}()

func addSeqTypes[V any](m map[hostSeqKey]reflect.Type) {
	v := reflect.TypeFor[V]()
	m[hostSeqKey{a: v}] = reflect.TypeFor[iter.Seq[V]]()
	m[hostSeqKey{a: reflect.TypeFor[int](), b: v}] = reflect.TypeFor[iter.Seq2[int, V]]()
	m[hostSeqKey{a: reflect.TypeFor[string](), b: v}] = reflect.TypeFor[iter.Seq2[string, V]]()
}

// hostBasicKinds maps the predeclared basic type names to their Go types.
var hostBasicKinds = map[string]reflect.Type{
	"bool": reflect.TypeFor[bool](), "string": reflect.TypeFor[string](),
	"int": reflect.TypeFor[int](), "int8": reflect.TypeFor[int8](), "int16": reflect.TypeFor[int16](),
	"int32": reflect.TypeFor[int32](), "rune": reflect.TypeFor[int32](), "int64": reflect.TypeFor[int64](),
	"uint": reflect.TypeFor[uint](), "uint8": reflect.TypeFor[uint8](), "byte": reflect.TypeFor[uint8](),
	"uint16": reflect.TypeFor[uint16](), "uint32": reflect.TypeFor[uint32](), "uint64": reflect.TypeFor[uint64](),
	"uintptr": reflect.TypeFor[uintptr](), "float32": reflect.TypeFor[float32](), "float64": reflect.TypeFor[float64](),
	"complex64": reflect.TypeFor[complex64](), "complex128": reflect.TypeFor[complex128](),
}

// IterSeqArity reports 1 for an iter.Seq typedef, 2 for iter.Seq2 and 0
// for anything else.
func IterSeqArity(td *TypeDef) int {
	if td == nil || td.Pkg == nil || td.Pkg.Path != "iter" {
		return 0
	}
	switch td.Name {
	case "Seq", "iter.Seq":
		return 1
	case "Seq2", "iter.Seq2":
		return 2
	}
	return 0
}

// IterSeqElemTypes returns the element typedefs of an iter.Seq ([V]) or
// iter.Seq2 ([K, V]) typedef; nil for anything else or an unbound
// element.
func IterSeqElemTypes(td *TypeDef) []*TypeDef {
	n := IterSeqArity(td)
	if n == 0 {
		return nil
	}
	names := []string{"V"}
	if n == 2 {
		names = []string{"K", "V"}
	}
	if len(td.TParams) == n {
		names = td.TParams
	}
	out := make([]*TypeDef, n)
	for i, tp := range names {
		et, _ := td.Binds[tp].(*TypeDef)
		if et == nil {
			return nil
		}
		out[i] = et
	}
	return out
}

// HostBasicType is the Go type of a predeclared basic typedef (int,
// string, ...); nil for any other type, including a type defined over
// one.
func HostBasicType(td *TypeDef) reflect.Type {
	if td == nil || td.Spec != nil || td.Pkg != nil || td.HostNew != nil || td.HostScalar != nil {
		return nil
	}
	return hostBasicKinds[td.Name]
}

// HostIterSeqType is the host iter.Seq[V] / iter.Seq2[K, V] type of a
// script seq typedef when its elements are predeclared basics the host
// binary has an instantiation for (Seq2 keyed by int or string); nil
// otherwise.
func HostIterSeqType(td *TypeDef) reflect.Type {
	els := IterSeqElemTypes(td)
	if els == nil {
		return nil
	}
	var key hostSeqKey
	for i, et := range els {
		rt := HostBasicType(et)
		if rt == nil {
			return nil
		}
		if i == 0 {
			key.a = rt
		} else {
			key.b = rt
		}
	}
	return hostSeqTypes[key]
}
