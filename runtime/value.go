// Values are boxed `any`; a tagged representation is a possible later
// optimization.

// Package runtime defines the values minigo scripts compute with, and the
// types the interpreter ([github.com/podhmo/minigo/vm]) and host code use
// to exchange them. Embedders meet these values through
// [github.com/podhmo/minigo.Engine.Call].
//
// [Value] lists the Go representation of each script value. Conventions
// that apply everywhere:
//   - A value of a declared type is a [Named] around the underlying value.
//     [Unwrap] removes only that tag; [DeepUnwrap] converts a whole
//     result, containers and pointers included, to plain Go values.
//   - A pointer is the [Cell] it points to, or a [FieldRef], [IndexRef] or
//     [DerefRef] for an address inside another value; read and write
//     through any of them with [Deref] and [SetRef].
//   - Structs and arrays are copied on assignment: store values through
//     [SetRef] or [Copy], or the target aliases the source.
//
// A [BuiltinFunc] receives evaluated arguments; a [SpecialFunc] receives
// its call unevaluated and evaluates parts of it on demand.
//
// Failures come in two kinds. A [Panic] is a script panic that script
// code can recover(); Go code raises one by calling panic with a *Panic.
// A [Trap] cannot be recovered and stops the script; an error returned by
// a builtin or special form becomes a Trap. Deferred calls still run while
// a Trap unwinds, but recover() inside them returns nil.
package runtime

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/syntax"
)

// Value is a script value. Its dynamic type is one of:
//
//	int64, float64, string, bool, Nil,
//	*Cell, *Slice, *Map, *Struct, *Chan, *TypeDef,
//	*Function, *Closure, *BoundMethod, *BuiltinFunc, *Tuple,
//	*Package, *Iterator, *GoValue, *TypedNil, *IfaceNil, *Named, *UConst
//
// Integers are normally held as int64 and floats as float64; a declared
// type keeps its identity through a [Named] wrapper ([Unwrap] removes it).
// Values without a script representation, such as complex numbers and
// host objects, are boxed in a [GoValue]. Nil is [NIL], or a [TypedNil] /
// [IfaceNil] when its type matters.
type Value = any

// Nil is the type of untyped nil; [NIL] is its only value.
type Nil struct{}

// NIL is the untyped nil value, and the zero value of interface types.
var NIL Value = Nil{}

// TypedNil is a nil value that knows its (non-interface) type: the zero of
// `var p *int`, `var s []int`, `(*int)(nil)`, `return nil` under a *T
// result. It still compares equal to nil — a nil pointer IS nil in Go —
// but carries enough type information to distinguish `x == nil` once it
// is stored into an interface-typed slot (see [IfaceNil]), to report the
// right zero through comma-ok asserts, and to panic on deref like a real
// nil pointer.
type TypedNil struct{ Typ *TypeDef }

// IfaceNil is a nil stored in an interface-typed slot. With a concrete
// Typ it is a [TypedNil] that crossed an interface boundary
// (`var x any = (*int)(nil)`, an `any` parameter, an `any` result): the
// interface records a dynamic type, so unlike a bare TypedNil it is NOT
// nil — `x == nil` is false and `x.(T)` sees the recorded dynamic type.
// With a nil or interface Typ it is the nil interface itself. Tell the
// cases apart with [IsNilIface], [BoxedNilTyp] and [IfaceTaggedNil].
type IfaceNil struct{ Typ *TypeDef }

// IfaceTaggedNil returns the interface typedef carried by a nil
// interface value — an [IfaceNil] whose static tag is an interface kind
// — else nil. An untagged IfaceNil (Typ == nil) has no tag to report,
// and a concrete tag means a boxed typed nil, not the nil interface.
func IfaceTaggedNil(v Value) *TypeDef {
	if in, ok := v.(*IfaceNil); ok && in.Typ != nil && in.Typ.Kind == KindInterface {
		return in.Typ
	}
	return nil
}

// BoxedNilTyp returns the dynamic typedef of a typed nil boxed in an
// interface — an [IfaceNil] whose tag is a concrete type — else nil.
// The distinction is observable: the boxed nil is NOT nil (it has a
// dynamic type), `x == nil` fails and `x.(T)` sees the tag.
func BoxedNilTyp(v Value) *TypeDef {
	if in, ok := v.(*IfaceNil); ok && in.Typ != nil && in.Typ.Kind != KindInterface {
		return in.Typ
	}
	return nil
}

// IsNilIface reports whether v is the nil interface itself — an
// [IfaceNil] with no dynamic type: untagged (Typ == nil) or carrying
// only an interface tag. A boxed typed nil reports false.
func IsNilIface(v Value) bool {
	if in, ok := v.(*IfaceNil); ok {
		return in.Typ == nil || in.Typ.Kind == KindInterface
	}
	return false
}

// ImplicitIndex marks the key of a positional element inside a mixed
// keyed/positional composite literal — `[8]int{3: 1, 2}` — where Go
// assigns it the running index (one past the previous element's).
type ImplicitIndex struct{}

// Named is a value of a declared type (`type MyInt int`, `type A B`):
// the underlying value plus the typedef it was declared under. Its
// declared identity survives reads — method dispatch, type asserts and
// generic inference see MyInt, not the bare int64 it stores. Everywhere
// else treats it as V: consumers (indexing, arithmetic, marshaling)
// [Unwrap] first. Construct Named values only with [Tag].
type Named struct {
	Typ *TypeDef // the declared typedef (a defined type, never a builtin)
	V   Value    // the underlying value
}

// Tag attaches the declared-type tag td to v. It is the one constructor
// of [Named] values: an existing tag is replaced rather than nested, so a
// Named never wraps another Named: `Tag(T, Named{U, x})` yields
// `Named{T, x}`. For a bound host type ([TypeDef.HostScalar] or
// [TypeDef.HostNew]) the payload is converted to the host's
// representation.
func Tag(td *TypeDef, v Value) *Named {
	sv := Unwrap(v)
	if td != nil && td.HostScalar != nil {
		sv = hostScalarValue(td, sv)
	} else if td != nil && td.HostNew != nil {
		sv = hostStringValue(td, sv)
	}
	return &Named{Typ: td, V: sv}
}

// byteValues holds one shared byte-tagged value per byte.
var byteValues = func() (vs [256]*Named) {
	for i := range vs {
		vs[i] = Tag(BasicTypedef("byte"), int64(i))
	}
	return vs
}()

// ByteValue returns the shared byte-tagged value for b. Bulk host []byte
// conversions use it instead of allocating a Named per element: a Named
// is never mutated in place (element stores replace the slot), so one
// value per byte can back every slice.
func ByteValue(b byte) *Named { return byteValues[b] }

// hostScalarValue canonicalizes a host-scalar typedef's payload — a bound
// stdlib named basic (time.Duration) keeps the raw host scalar so
// arithmetic and formatting behave like the host value. A bind that
// produced the plain-kind equivalent (a literal int64 zero or init)
// converts to the host type so method dispatch and %v see the real type.
func hostScalarValue(td *TypeDef, v Value) Value {
	if gv, ok := v.(*GoValue); ok {
		v = gv.V
	}
	rt := reflect.TypeOf(td.HostScalar)
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Type() == rt {
		return v
	}
	if rv.Kind() == rt.Kind() && rv.Type().ConvertibleTo(rt) {
		return rv.Convert(rt).Interface()
	}
	return v
}

// HostScalarOf converts v, the payload of a value of type td, to the
// host Go type in [TypeDef.HostScalar]: a stored int64 or an untyped
// [UConst] becomes a real time.Month or time.Duration, so host methods
// and formatting see the host type. ok is false when td has no
// HostScalar or v does not convert.
func HostScalarOf(td *TypeDef, v Value) (any, bool) {
	// The same canonicalization hostScalarValue applies at Tag time,
	// deferred to the read because a Named{td, UConst} keeps the constant
	// domain.
	rt := reflect.TypeOf(td.HostScalar)
	if rt == nil {
		return nil, false
	}
	if u, ok := v.(*UConst); ok {
		mv, err := UConstNative(u)
		if err != nil {
			return nil, false
		}
		v = mv
	}
	if gv, ok := v.(*GoValue); ok {
		v = gv.V
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil, false
	}
	if rv.Type() == rt {
		return v, true
	}
	// same kind converts directly; a numeric payload also converts
	// across widths — the script repr stores every integer as int64
	// while time.Month's underlying is int (kind Int vs Int64).
	if rv.Type().ConvertibleTo(rt) &&
		(rv.Kind() == rt.Kind() || (numericScalarKind(rv.Kind()) && numericScalarKind(rt.Kind()))) {
		return rv.Convert(rt).Interface(), true
	}
	return nil, false
}

// numericScalarKind reports the arithmetic families a scalar payload may
// cross-width-convert within — int*, uint*, float*, complex* — keeping
// string/bool payloads from converting into a numeric host type.
func numericScalarKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	}
	return false
}

// hostStringValue canonicalizes a string-kind host basic's payload to the
// script string — the mirror of hostScalarValue in the other direction. A
// bound typedef like reflect.StructTag is a stdlib scalar but every
// operation Go defines on it besides Get/Lookup is a string op
// (len, indexing, range, comparisons, concat, conversions); keeping the
// payload a script string keeps all of those working while member access
// materializes the host type to reach its method set. Non-string payloads
// (int into a string type) and non-string host types pass through.
func hostStringValue(td *TypeDef, v Value) Value {
	rt := reflect.TypeOf(td.HostNew())
	if rt == nil || rt.Kind() != reflect.String {
		return v
	}
	if gv, ok := v.(*GoValue); ok {
		v = gv.V
	}
	if _, ok := v.(string); ok {
		return v
	}
	if rv := reflect.ValueOf(v); rv.IsValid() && rv.Type().ConvertibleTo(rt) {
		// same-kind host strings unwrap to the script string; an int
		// payload converts by Go's numeric-to-string rule — StructTag(65).
		return rv.Convert(rt).String()
	}
	return v
}

// TagOf reads the declared-type tag of a [Named] v, or nil when v is
// untagged. Use it where the tag itself matters — method dispatch, type
// assertions, formatting — and [Unwrap] where the underlying value does.
func TagOf(v Value) *TypeDef {
	if n, ok := v.(*Named); ok {
		return n.Typ
	}
	return nil
}

// Unwrap removes every [Named] tag from v; any other value passes through
// unchanged. It does not look inside containers or references; use
// [DeepUnwrap] for a host-side Go value.
func Unwrap(v Value) Value {
	for {
		if n, ok := v.(*Named); ok {
			v = n.V
			continue
		}
		return v
	}
}

// Zero returns the zero value of a typedef: a [Struct] whose fields are
// [NIL], a [TypedNil] for nilable kinds (pointer, slice, map, chan, func),
// NIL for interface types and a nil td, the literal zero for basic types,
// and a tagged [GoValue] for host types with [TypeDef.HostNew]. Named
// basics whose underlying type fails to resolve yield a TypedNil rather
// than a fake scalar. Struct fields are not zeroed by their declared
// types; [VMCaller.Zero] does that.
func Zero(td *TypeDef) Value {
	if td == nil {
		return NIL
	}
	if td.HostNew != nil {
		if k := td.HostType().Kind(); k == reflect.Slice || k == reflect.Map {
			// a bound named slice or map (net.IP) zeros to nil like
			// in Go — HostNew's empty box would compare != nil. The
			// typed nil still dispatches the host methods.
			return &TypedNil{Typ: td}
		}
		// keep the declared tag so %T/TypeOf read T while the box is
		// pointer-shaped (`var b bytes.Buffer` → bytes.Buffer, not
		// *bytes.Buffer).
		return Tag(td, &GoValue{V: td.HostNew()})
	}
	switch td.Kind {
	case KindInterface:
		return NIL
	case KindSlice, KindMap, KindChan, KindFunc, KindPointer:
		return &TypedNil{Typ: td}
	case KindStruct:
		s := &Struct{Def: td, Fields: make([]Value, len(td.Fields))}
		for i := range s.Fields {
			s.Fields[i] = NIL
		}
		return s
	}
	switch td.Name {
	case "string":
		return ""
	case "bool":
		return false
	case "float32", "float64":
		return float64(0)
	case "complex64":
		return &GoValue{V: complex64(0)}
	case "complex128":
		return &GoValue{V: complex128(0)}
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "byte", "rune", "uintptr":
		return int64(0)
	}
	if td.Kind == KindNamedBasic {
		// `type S string` zeros as the underlying literal: the declared
		// name is the new name, so read the underlying ident instead.
		if id, ok := td.Anon.(*ast.Ident); ok {
			if z, ok := basicZero(id.Name); ok {
				return z
			}
		}
		// the underlying type did not resolve (missing import, unbound
		// type parameter): keep a typed nil hole rather than faking a
		// scalar zero.
		return &TypedNil{Typ: td}
	}
	return NIL
}

func basicZero(name string) (Value, bool) {
	switch name {
	case "string":
		return "", true
	case "bool":
		return false, true
	case "float32", "float64":
		return float64(0), true
	case "complex64":
		return &GoValue{V: complex64(0)}, true
	case "complex128":
		return &GoValue{V: complex128(0)}, true
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "byte", "rune", "uintptr":
		return int64(0), true
	}
	return nil, false
}

// Cell is a mutable slot. Every declared variable is a cell, and a
// pointer value is the *Cell it points to (or a [FieldRef], [IndexRef] or
// [DerefRef] for addresses inside other values). Read and write any of
// them with [Deref] and [SetRef].
type Cell struct {
	Elem Value
	// Typ is the declared type of the slot when one is known (`var x T`,
	// a typed parameter, a named result, new(T)): stores into the cell
	// coerce incoming values to it, so `x = v` gets the same assignability
	// check as `var x T = v`.
	Typ *TypeDef
	// ReadOnly marks a constant binding: stores through the cell trap.
	ReadOnly bool
}

// FieldRef is the pointer `&s.f` to a struct field. Base is resolved at
// each access (a struct value or a pointer to one), and Name may select a
// promoted field of an embedded struct.
type FieldRef struct {
	Base Value
	Name string
}

// structOf resolves the base to the struct being referenced.
func (r *FieldRef) structOf() *Struct {
	return StructOf(r.Base)
}

// StructOf resolves v to the [Struct] it reaches through [Named] tags and
// pointers, or nil.
func StructOf(v Value) *Struct {
	for {
		if s, ok := v.(*Struct); ok {
			return s
		}
		if n, ok := v.(*Named); ok {
			v = n.V
			continue
		}
		dv, ok := Deref(v)
		if !ok {
			return nil
		}
		v = dv
	}
}

// promotedStructs expands one BFS level of embedded fields — Go
// resolves promoted members by depth (shallowest wins), not by
// declaration order.
func promotedStructs(level []*Struct) (next []*Struct) {
	for _, st := range level {
		if st == nil || st.Def == nil {
			continue
		}
		for _, i := range st.Def.EmbedIdx {
			if i < len(st.Fields) {
				if emb := StructOf(st.Fields[i]); emb != nil {
					next = append(next, emb)
				}
			}
		}
	}
	return next
}

// fieldHits collects (struct, index) pairs named by r.Name across one
// BFS level — every hit here shares the same promotion depth.
func (r *FieldRef) fieldHits(level []*Struct) (hits []struct {
	st  *Struct
	idx int
}) {
	for _, st := range level {
		if st == nil || st.Def == nil {
			continue
		}
		for i, n := range st.Def.Fields {
			if n == r.Name {
				hits = append(hits, struct {
					st  *Struct
					idx int
				}{st, i})
			}
		}
	}
	return hits
}

// find resolves the promoted field target breadth-first like Go: the
// shallowest match wins and a same-depth tie is ambiguous.
func (r *FieldRef) find() (st *Struct, idx int, ok bool) {
	s := r.structOf()
	if s == nil {
		return nil, 0, false
	}
	// a direct field needs no BFS: depth 0 has a single struct and
	// field names within it are unique (a blank `_` is never selected).
	if s.Def != nil {
		for i, n := range s.Def.Fields {
			if n == r.Name {
				return s, i, true
			}
		}
	}
	level := []*Struct{s}
	for depth := 0; len(level) > 0 && depth < 32; depth++ {
		switch hits := r.fieldHits(level); len(hits) {
		case 0:
			level = promotedStructs(level)
		case 1:
			return hits[0].st, hits[0].idx, true
		default:
			panic(&Panic{Value: fmt.Sprintf("ambiguous selector %s", r.Name)})
		}
	}
	return nil, 0, false
}

// Find resolves the field slot the ref points at: the owning struct and
// field index after embedded promotion. Two refs to the same storage
// yield the same pair. It panics on an ambiguous promoted selector.
func (r *FieldRef) Find() (*Struct, int, bool) { return r.find() }

// Get reads the field value; ok is false when the field cannot be
// resolved.
func (r *FieldRef) Get() (Value, bool) {
	if st, idx, ok := r.find(); ok {
		return st.Fields[idx], true
	}
	return nil, false
}

// Set writes the field value as is (no copy; see [SetRef]) and reports
// whether the field was resolved.
func (r *FieldRef) Set(v Value) bool {
	if st, idx, ok := r.find(); ok {
		st.Fields[idx] = v
		return true
	}
	return false
}

// DerefRef is the location `*p` names when p itself is stored in a
// tracked location: the pointee is resolved at each access, so in
// `*p = v` a right-hand side that reseats p still writes the new pointee.
type DerefRef struct {
	Ptr Value // the storage ref whose value is the pointer
}

// loc resolves the pointer's storage to the current pointee location.
func (r *DerefRef) loc() (Value, bool) { return Deref(r.Ptr) }

// Get reads the value at the pointee location.
func (r *DerefRef) Get() (Value, bool) {
	loc, ok := r.loc()
	if !ok {
		return nil, false
	}
	return Deref(loc)
}

// Set writes through the resolved pointee location.
func (r *DerefRef) Set(v Value) bool {
	loc, ok := r.loc()
	if !ok {
		return false
	}
	return SetRef(loc, v)
}

// SharedElem reports whether v, read as a map element, refers to shared
// storage: a slice, map, chan, func or pointer. Only then can a write
// like `m[k][i] = v` reach it; arrays, structs, scalars and strings read
// out of a map are copies.
func SharedElem(v Value) bool {
	for {
		switch x := v.(type) {
		case *Named:
			if arrayTypedef(x.Typ) {
				return false // `type A [N]T` stores an array copy
			}
			v = x.V
			continue
		case *Slice:
			return !arrayTypedef(x.Typ)
		case *Map, *Chan,
			*Function, *Closure, *BoundMethod, *BuiltinFunc,
			*Cell, *FieldRef, *IndexRef, *DerefRef:
			return true
		case *GoValue:
			// host values share only through pointers, like Go's
			// implicit deref of `m[k]` on a map of *T.
			return x.V != nil && reflect.TypeOf(x.V).Kind() == reflect.Pointer
		case *TypedNil, *IfaceNil:
			var td *TypeDef
			switch t := x.(type) {
			case *TypedNil:
				td = t.Typ
			case *IfaceNil:
				td = t.Typ
			}
			if td == nil || arrayTypedef(td) {
				return false
			}
			switch td.Kind {
			case KindSlice, KindMap, KindPointer, KindChan, KindFunc:
				return true
			}
			return false
		}
		return false
	}
}

// IndexRef is the pointer `&s[i]` to a slice or array element. Over a
// map it names m[k] for interior writes and only resolves when the
// element is reference-shaped (see [SharedElem]), since map elements are
// not addressable in Go.
type IndexRef struct {
	Base Value
	// Key is the index operand as evaluated: a named key keeps its
	// Named tag, so a map store through the ref still type-checks it
	// against the declared key type. Hash or index with Unwrap(Key).
	Key Value
}

// container resolves the base to the referenced slice or map — one
// shared walk for sliceOf and mapOf. Reaching the other container
// kind still reports the caller's kind as absent: a map base cannot
// resolve a slice element.
func (r *IndexRef) container() Value {
	v := r.Base
	for {
		switch v.(type) {
		case *Slice, *Map:
			return v
		}
		if n, ok := v.(*Named); ok {
			v = n.V
			continue
		}
		dv, ok := Deref(v)
		if !ok {
			return nil
		}
		v = dv
	}
}

// sliceOf resolves the base to the slice being referenced.
func (r *IndexRef) sliceOf() *Slice {
	s, _ := r.container().(*Slice)
	return s
}

// mapOf resolves the base to the map being referenced.
func (r *IndexRef) mapOf() *Map {
	m, _ := r.container().(*Map)
	return m
}

// Slice resolves the base to the referenced slice, or nil when it is not
// one.
func (r *IndexRef) Slice() *Slice { return r.sliceOf() }

// Map resolves the base to the referenced map, or nil when it is not
// one. A non-nil result means the ref names a map element m[k], which Go
// reads as a copy rather than an addressable slot.
func (r *IndexRef) Map() *Map { return r.mapOf() }

// Get reads the element value; ok is false for an out-of-range index, a
// missing map key or a non-reference map element.
func (r *IndexRef) Get() (Value, bool) {
	if m := r.mapOf(); m != nil {
		// a missing key reports no value — callers needing the zero
		// go through the VM's index path, which knows the elem typedef.
		v, ok := m.Get(Unwrap(r.Key))
		if !ok || !SharedElem(v) {
			// interior access on a non-reference element must not
			// resolve: `m[k]` reads a copy in Go, so writes like
			// `m[k][i] = v` on a map of arrays are compile-time
			// rejections, and reporting no location keeps the store
			// paths on their traps.
			return nil, false
		}
		return v, true
	}
	s := r.sliceOf()
	i, ok := Unwrap(r.Key).(int64)
	if s == nil || !ok || i < 0 || i >= int64(len(s.Elems)) {
		return nil, false
	}
	return s.Elems[i], true
}

// Set writes the element value as is (no copy; see [SetRef]). A map key
// is inserted if missing; ok is false when the base is neither a slice
// nor a map, or the index is out of range.
func (r *IndexRef) Set(v Value) bool {
	if m := r.mapOf(); m != nil {
		k := r.Key
		if !m.IfaceKey {
			k = Unwrap(k)
		}
		m.Insert(k, v)
		return true
	}
	s := r.sliceOf()
	i, ok := Unwrap(r.Key).(int64)
	if s == nil || !ok || i < 0 || i >= int64(len(s.Elems)) {
		return false
	}
	s.Elems[i] = v
	return true
}

// Deref reads through a pointer value one level: a [Cell], [FieldRef],
// [IndexRef] or [DerefRef]. ok is false for non-pointers and for
// references that do not resolve. A [Named] value is transparent: a
// named pointer type dereferences, a named scalar does not.
func Deref(v Value) (Value, bool) {
	switch r := v.(type) {
	case *Cell:
		return r.Elem, true
	case *FieldRef:
		return r.Get()
	case *IndexRef:
		return r.Get()
	case *DerefRef:
		return r.Get()
	case *Named:
		return Deref(r.V)
	}
	return nil, false
}

// Copy returns v as a Go assignment would store it: structs and arrays
// are copied recursively, while slices, maps, pointers, channels and
// funcs are shared. A [Named] value keeps its tag. Apply it whenever a
// value is stored, or the target aliases the source's fields.
func Copy(v Value) Value {
	switch x := v.(type) {
	case *Struct:
		cp := &Struct{Def: x.Def, Fields: make([]Value, len(x.Fields))}
		for i, e := range x.Fields {
			cp.Fields[i] = Copy(e)
		}
		return cp
	case *Slice:
		// arrays copy on assignment like structs — nested arrays copy
		// recursively. Plain slices share their backing (Go semantics).
		if arrayTypedef(x.Typ) {
			el := make([]Value, len(x.Elems))
			for i, e := range x.Elems {
				el[i] = Copy(e)
			}
			return &Slice{Elems: el, Typ: x.Typ}
		}
		return v
	case *Named:
		// a HostNew struct's payload is a `*T` box — assignment copies
		// the struct like gc (`var b bytes.Buffer = a` must not alias
		// a's storage). The tag is the only reliable signal: a bare
		// *T GoValue may be a host-returned pointer that must share.
		if x.Typ != nil && x.Typ.Kind == KindStruct && x.Typ.HostNew != nil {
			if cp, ok := HostStructCopy(x.V); ok {
				return Tag(x.Typ, cp)
			}
		}
		// an immutable scalar payload needs no new box: a Named is never
		// mutated in place, so the copy can share x (make/copy/append
		// copy every element of a []byte through here).
		switch x.V.(type) {
		case int64, float64, bool, string, complex128:
			return x
		}
		// assignment copies the underlying value but keeps the declared tag
		return Tag(x.Typ, Copy(x.V))
	}
	return v
}

// HostStructCopy copies the struct behind a [GoValue] holding a non-nil
// pointer to a struct, returning a new box. Unlike [Copy], which copies a
// host struct only when it is tagged with a [TypeDef.HostNew] struct type
// (a bare *T box may be a host pointer that must stay shared), it copies
// unconditionally; the caller decides that v is a struct value. ok is
// false for any other value, which the caller should store unchanged.
func HostStructCopy(v Value) (Value, bool) {
	gv, ok := v.(*GoValue)
	if !ok {
		return nil, false
	}
	rv := reflect.ValueOf(gv.V)
	if rv.Kind() != reflect.Ptr || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	cp := reflect.New(rv.Elem().Type())
	cp.Elem().Set(rv.Elem())
	return &GoValue{V: cp.Interface()}, true
}

// SetRef stores val through a pointer value ([Cell], [FieldRef],
// [IndexRef] or [DerefRef], possibly [Named]) and reports whether it did.
// val is stored as a [Copy], like a Go assignment.
func SetRef(v, val Value) bool {
	val = Copy(val)
	switch r := v.(type) {
	case *Cell:
		r.Elem = val
		return true
	case *FieldRef:
		return r.Set(val)
	case *IndexRef:
		return r.Set(val)
	case *DerefRef:
		return r.Set(val)
	case *Named:
		return SetRef(r.V, val)
	}
	return false
}

// mapKey is the canonical form of a composite map key: structs and
// arrays compare by content in Go, so the Pairs table keys them by a
// type-tagged rendering of their fields rather than pointer identity.
type mapKey struct {
	typ  string
	repr string
}

// CanonicalKey converts a script map key to the Go-comparable form that
// keys [Map.Pairs], so composite keys compare by content. Scalars pass
// through; pointers and channels compare by identity; structs and arrays
// compare by type and contents. A key containing NaN never equals any
// other key, itself included, so calling CanonicalKey twice on it yields
// different results. Unhashable keys (slices, maps, funcs) panic with a
// [*Panic] like Go's runtime.
func CanonicalKey(v Value) Value {
	switch x := Unwrap(v).(type) {
	case *Struct:
		// an unhashable field panics naming the outer struct type —
		// Go's "hash of unhashable type main.T", nil or not.
		for _, e := range x.Fields {
			if unhashableKey(e) {
				panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + msgTypeName(x.Def)}})
			}
		}
		var sb strings.Builder
		sb.WriteString(typeTagOf(x.Def))
		writeFieldsRepr(&sb, x)
		return mapKey{typ: typeTagOf(x.Def), repr: sb.String()}
	case *Slice:
		// only fixed-size arrays are comparable; a slice key panics
		// like Go's runtime unhashable-type check.
		if !arrayTypedef(x.Typ) {
			panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + msgTypeName(x.Typ)}})
		}
		for _, e := range x.Elems {
			if unhashableKey(e) {
				panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + msgTypeName(x.Typ)}})
			}
		}
		var sb strings.Builder
		writeKeyRepr(&sb, x.Elems)
		return mapKey{typ: typeTagOf(x.Typ), repr: sb.String()}
	case *Map:
		panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + msgTypeName(x.Typ)}})
	case *Function, *Closure, *BoundMethod, *BuiltinFunc:
		panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type func()"}})
	case *GoValue:
		if !reflect.TypeOf(x.V).Comparable() {
			panic(&Panic{Value: &RuntimeError{Msg: fmt.Sprintf("hash of unhashable type %T", x.V)}})
		}
		return x.V
	case *TypedNil:
		// a nil slice/map/func key is still unhashable — the TYPE
		// decides, like Go's runtime check.
		if unhashableKind(x.Typ) {
			panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + msgTypeName(x.Typ)}})
		}
		return mapKey{typ: typeTagOf(x.Typ), repr: "nil"}
	case *IfaceNil:
		if unhashableKind(x.Typ) {
			panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + msgTypeName(x.Typ)}})
		}
		// A nil interface records no dynamic type — m[any(e)] on a
		// nil error and m[nil] hash to the same eface key in Go — so
		// it canonicalizes like bare NIL, not under its declared
		// interface's tag. A boxed typed nil (IfaceNil{*T}) keeps
		// its tag: (*T)(nil) is a real dynamic type.
		if x.Typ == nil || x.Typ.Kind == KindInterface {
			return NIL
		}
		return mapKey{typ: typeTagOf(x.Typ), repr: "nil"}
	case *Cell, *FieldRef, *IndexRef, *Chan:
		// pointer-shaped keys hash by identity — the wrapper itself
		// is comparable and stable.
		return x
	case float32:
		// a float32 key hashes as float64: the declared float32 key type
		// may unwrap to either width, and the host map compares them
		// equal only in one canonical form.
		return CanonicalKey(float64(x))
	case float64:
		// NaN keys store under a fresh nonce: a lookup can never match,
		// exactly like Go's never-equal map semantics.
		if x != x {
			return mapKey{typ: "float64", repr: fmt.Sprintf("NaN#%d", nextKeyNonce())}
		}
		return x
	case complex64:
		r, i := real(x), imag(x)
		if math.IsNaN(float64(r)) || math.IsNaN(float64(i)) {
			return mapKey{typ: "complex64", repr: fmt.Sprintf("NaN#%d", nextKeyNonce())}
		}
		return x
	case complex128:
		if math.IsNaN(real(x)) || math.IsNaN(imag(x)) {
			return mapKey{typ: "complex128", repr: fmt.Sprintf("NaN#%d", nextKeyNonce())}
		}
		return x
	default:
		return x
	}
}

// opaqueKeyOrigs remembers the script value each OpaqueKey canonical
// form was rendered from, so a container handing the key back (sync.Map
// Range callbacks, a loaded CAS comparand) restores the original instead
// of surfacing the rendered key. Canonical forms are content-equal, so
// two equal script values share one entry — the same key in gc too.
// Entries live for the process: host containers offer no eviction
// callback worth tracking.
var opaqueKeyOrigs sync.Map // mapKey -> Value

// OpaqueKey converts v for an `any` argument that a host container
// hashes or compares — sync.Map keys, context keys, CompareAndSwap's
// comparand. Equal script values yield equal keys: a [Named] value keeps
// its type, so any(uint16(8)) and any(int(8)) stay distinct, and
// composite values compare by content as in [CanonicalKey]. Unhashable
// values panic like Go's map-key check. When the container hands a key
// back, [OpaqueKeyOrig] recovers the script value; [TryOpaqueKey] does
// not panic.
func OpaqueKey(v Value) Value {
	ck := opaqueKeyOf(v)
	if _, ok := ck.(mapKey); ok {
		// the orig restores what OpaqueKeyOrig hands back — gc's
		// boxing snapshots the value, so the stored orig is a copy:
		// mutating the source var after the store must not leak.
		opaqueKeyOrigs.Store(ck, Copy(v))
	}
	return ck
}

// TryOpaqueKey is [OpaqueKey] without the panic: ok is false for
// unhashable values. Use it for `any` arguments that are stored but not
// necessarily hashed (a sync.Map value, CompareAndSwap's new value),
// passing an unhashable value through unchanged.
func TryOpaqueKey(v Value) (ck Value, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, isPanic := r.(*Panic); !isPanic {
				panic(r)
			}
			ck, ok = nil, false
		}
	}()
	return OpaqueKey(v), true
}

// OpaqueKeyOrig returns the script value a key from [OpaqueKey] was made
// from. ok is false when v is not such a key; that includes scalars,
// which OpaqueKey passes through unchanged.
func OpaqueKeyOrig(v Value) (Value, bool) {
	if _, ok := v.(mapKey); !ok {
		return nil, false
	}
	orig, ok := opaqueKeyOrigs.Load(v)
	if !ok {
		return nil, false
	}
	return orig, true
}

// opaqueKeyOf folds v to its key form: a Named keeps its tag (interface
// equality is (type, payload), so the tag belongs in the key while the
// payload still renders by content); everything else takes
// CanonicalKey's walk.
func opaqueKeyOf(v Value) Value {
	if u, ok := v.(*UConst); ok {
		// a typed constant still rides the const domain: materialize or
		// each key renders a fresh UConst and never hits.
		mv, err := UConstNative(u)
		if err != nil {
			panic(&Panic{Value: &RuntimeError{Msg: err.Error()}})
		}
		v = mv
	}
	if n, ok := v.(*Named); ok {
		nv := n.V
		if u, ok := nv.(*UConst); ok {
			mv, err := UConstNative(u)
			if err != nil {
				panic(&Panic{Value: &RuntimeError{Msg: err.Error()}})
			}
			nv = mv
		}
		if unhashableKey(nv) {
			panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + msgTypeName(n.Typ)}})
		}
		var sb strings.Builder
		sb.WriteString(typeTagOf(n.Typ))
		writeKeyRepr(&sb, []Value{nv})
		return mapKey{typ: typeTagOf(n.Typ), repr: sb.String()}
	}
	return CanonicalKey(v)
}

// WrapInt64 truncates an Int constant to its low 64 bits read as a
// two's-complement int64 — the value a 64-bit integer operation leaves
// (`int(1) << 70` is 0, `int(1) << 63` is MinInt64).
func WrapInt64(cv constant.Value) int64 {
	if i, ok := constant.Int64Val(cv); ok {
		return i
	}
	mod := new(big.Int).Lsh(big.NewInt(1), 64)
	x := new(big.Int).Mod(constantBigInt(cv), mod) // in [0, 2^64)
	return int64(x.Uint64())
}

// constantBigInt reads an Int constant as a *big.Int.
func constantBigInt(cv constant.Value) *big.Int {
	switch x := constant.Val(cv).(type) {
	case int64:
		return big.NewInt(x)
	case *big.Int:
		return x
	}
	return new(big.Int)
}

// UConstNative converts an untyped constant to a Go value of its default
// type: bool, string, int64, float64 or complex128. It reports Go's
// overflow error when the constant does not fit, and folds -0 to +0 (see
// [CanonConstZero]). The result is a plain Go value: the caller adds the
// rune tag for a [UConst.Rune] constant, and the [GoValue] box a
// complex128 needs to be a [Value].
func UConstNative(u *UConst) (Value, error) {
	switch u.V.Kind() {
	case constant.Bool:
		return constant.BoolVal(u.V), nil
	case constant.String:
		return constant.StringVal(u.V), nil
	case constant.Int:
		if i, ok := constant.Int64Val(u.V); ok {
			return i, nil
		}
		if uv, ok := constant.Uint64Val(u.V); ok && uv <= math.MaxInt64 {
			return int64(uv), nil
		}
		if u.Wrap {
			return WrapInt64(u.V), nil
		}
		return nil, fmt.Errorf("constant %s overflows int", u.V)
	case constant.Float:
		f, _ := constant.Float64Val(u.V)
		if math.IsInf(f, 0) {
			return nil, fmt.Errorf("constant %s overflows float64", u.V)
		}
		return CanonConstZero(f), nil
	case constant.Complex:
		re, _ := constant.Float64Val(constant.Real(u.V))
		im, _ := constant.Float64Val(constant.Imag(u.V))
		return complex(CanonConstZero(re), CanonConstZero(im)), nil
	}
	return nil, fmt.Errorf("cannot materialize constant %s", u.V)
}

// CanonConstZero folds -0 to +0. Go constants have no negative zero —
// literal -0.0 and underflowing magnitudes like -1e-10000 both read +0 —
// so apply it wherever a constant becomes a float64, but not to values
// computed at run time.
func CanonConstZero(f float64) float64 {
	if f == 0 {
		return 0
	}
	return f
}

// msgTypeName renders a typedef for panic text — it delegates to the
// canonical display speller (package-name qualifier like Go's "main.T",
// canonical anonymous spellings).
func msgTypeName(td *TypeDef) string {
	return DisplayName(td)
}

// unhashableKind reports whether a typedef's kind is unhashable — a
// nil slice/map/func still fails Go's map-key check by type.
func unhashableKind(td *TypeDef) bool {
	return td != nil && (td.Kind == KindSlice || td.Kind == KindMap || td.Kind == KindFunc)
}

// unhashableKey reports whether hashing v as part of a composite key
// must panic — non-array slices, maps, funcs and their typed nils.
func unhashableKey(v Value) bool {
	switch x := Unwrap(v).(type) {
	case *Slice:
		return !arrayTypedef(x.Typ)
	case *Map, *Function, *Closure, *BoundMethod, *BuiltinFunc:
		return true
	case *TypedNil:
		return unhashableKind(x.Typ)
	case *IfaceNil:
		// a boxed nil slice/map/func is unhashable through the
		// interface box too.
		return unhashableKind(x.Typ)
	}
	return false
}

var keyNonce atomic.Int64

// writeKeyRepr renders composite key contents canonically: "K{1,2}".
// A NaN field anywhere emits a unique nonce instead — Go maps never
// match a NaN-bearing key, even against itself.
func writeKeyRepr(sb *strings.Builder, elems []Value) {
	sb.WriteByte('{')
	for i, e := range elems {
		if i > 0 {
			sb.WriteByte(',')
		}
		writeKeyElem(sb, e)
	}
	sb.WriteByte('}')
}

func writeKeyElem(sb *strings.Builder, v Value) {
	switch x := Unwrap(v).(type) {
	case *Struct:
		sb.WriteString(typeTagOf(x.Def))
		writeFieldsRepr(sb, x)
	case *Slice:
		if !arrayTypedef(x.Typ) {
			panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + msgTypeName(x.Typ)}})
		}
		writeKeyRepr(sb, x.Elems)
	case float64:
		if x != x { // NaN
			sb.WriteString(fmt.Sprintf("NaN#%d", nextKeyNonce()))
			return
		}
		fmt.Fprintf(sb, "%v", x)
	case *Cell, *FieldRef, *IndexRef, *Chan:
		fmt.Fprintf(sb, "%p", x)
	case *GoValue:
		// a host named basic in a slot of its own type renders like
		// its source twin's payload: K{fi.Mode().Type()} finds
		// K{fs.ModeDir} for `type K struct{ M fs.FileMode }`
		// (FileMode's %#v is hex, the twin's decimal). An
		// interface-typed slot renders the dynamic type too
		// (writeFieldsRepr).
		if p, _, ok := HostNamedScalar(x); ok {
			writeKeyElem(sb, p)
			return
		}
		writeKeyElem(sb, fmt.Sprintf("%#v", x.V))
	case *IfaceNil:
		// a nil interface records no dynamic type, so one that
		// crossed (or never left) an interface-typed boundary folds
		// to bare nil like CanonicalKey. A boxed typed nil keeps
		// its tag — struct{any}{(*int)(nil)} and struct{any}{nil}
		// are distinct Go keys.
		if IsNilIface(x) {
			sb.WriteString("nil")
		} else {
			sb.WriteString(typeTagOf(x.Typ))
			sb.WriteString(":nil")
		}
	case *TypedNil, Nil:
		sb.WriteString("nil")
	default:
		fmt.Fprintf(sb, "%v", x)
	}
}

// writeFieldsRepr renders a struct key's fields like [writeKeyRepr],
// except that a field declared with an interface type renders its
// dynamic type with the value ([IfaceKeyOf]) — K{any(M(2))} and
// K{any(uint32(2))} are distinct Go keys. Field types come from the
// engine's cache ([TypeDef.CachedFieldTypes]); without one every field
// renders untyped.
func writeFieldsRepr(sb *strings.Builder, x *Struct) {
	var fts []*TypeDef
	if x.Def != nil {
		fts, _ = x.Def.CachedFieldTypes()
	}
	if fts == nil {
		writeKeyRepr(sb, x.Fields)
		return
	}
	sb.WriteByte('{')
	for i, e := range x.Fields {
		if i > 0 {
			sb.WriteByte(',')
		}
		if i < len(fts) && fts[i] != nil && fts[i].Kind == KindInterface {
			if tk, ok := typedScalarKeyOf(e); ok {
				sb.WriteString(tk.typ)
				sb.WriteByte(':')
				writeKeyElem(sb, tk.v)
				continue
			}
		}
		writeKeyElem(sb, e)
	}
	sb.WriteByte('}')
}

// typedKey keys a scalar by its dynamic type in an interface-keyed map
// ([IfaceKeyOf]): the payload is the canonical script scalar, typ the
// type's identity (a basic name, a declared type's tag, or a host
// type's package path and name).
type typedKey struct {
	typ string
	v   Value
}

// IfaceKeyOf is [CanonicalKey] for a key of an interface-typed map
// ([Map.IfaceKey]): Go keys an interface by (dynamic type, value), so a
// scalar's type tag joins the key — map[any]int{int8(1): 1}[1] misses,
// and a host fs.FileMode (os.Stat's Mode) keys apart from uint32 and
// from reflect.Kind with the same payload. A boxed host named basic and
// a value of its source twin or bound typedef share one key, so
// map[any]int{fs.ModeDir: 1}[fi.Mode().Type()] hits. Values of the
// default types (int, float64, string, bool, complex128) key like
// CanonicalKey; composites already key by type.
func IfaceKeyOf(v Value) Value {
	if tk, ok := typedScalarKeyOf(v); ok {
		if ck, isNaN := tk.v.(mapKey); isNaN {
			return ck
		}
		return tk
	}
	return CanonicalKey(v)
}

// typedScalarKeyOf is the typed key of a non-default-typed scalar —
// see [IfaceKeyOf]; ok is false for anything else.
func typedScalarKeyOf(v Value) (typedKey, bool) {
	switch x := v.(type) {
	case *Named:
		if x.Typ == nil {
			return typedKey{}, false
		}
		p := x.V
		if u, ok := p.(*UConst); ok {
			mv, err := UConstNative(u)
			if err != nil {
				return typedKey{}, false
			}
			p = Unwrap(mv)
		}
		var typ string
		switch {
		case x.Typ.HostScalar != nil:
			typ = hostTypeTag(reflect.TypeOf(x.Typ.HostScalar))
			if hp, _, ok := HostNamedPayload(p); ok {
				p = hp
			}
		case x.Typ.Spec == nil && x.Typ.Anon == nil && basicTypedefName(x.Typ.Name):
			typ = CanonicalBasicName(x.Typ.Name)
			switch typ {
			case "int", "float64", "string", "bool", "complex128":
				return typedKey{}, false // the default types key untagged
			}
		default:
			typ = typeTagOf(x.Typ)
		}
		switch p.(type) {
		case int64, float64, float32, string, bool, complex64, complex128:
			return typedKey{typ: typ, v: CanonicalKey(p)}, true
		}
		return typedKey{}, false
	case *GoValue:
		if p, rt, ok := HostNamedScalar(x); ok {
			return typedKey{typ: hostTypeTag(rt), v: p}, true
		}
	case int64, float64, string, bool, complex128, *Struct, *Slice:
		return typedKey{}, false
	case float32:
		return typedKey{typ: "float32", v: CanonicalKey(float64(x))}, true
	default:
		// a raw host scalar (a bound typedef's payload that lost its
		// tag) keys like its boxed form.
		if p, rt, ok := HostNamedPayload(x); ok {
			return typedKey{typ: hostTypeTag(rt), v: p}, true
		}
	}
	return typedKey{}, false
}

// hostTypeTag spells a named host type like [typeTagOf] spells its
// source twin: "io/fs.FileMode".
func hostTypeTag(rt reflect.Type) string {
	return rt.PkgPath() + "." + rt.Name()
}

func nextKeyNonce() int64 {
	return keyNonce.Add(1)
}

// PtrArrayType returns the array type AST of a *[N]T typedef, or nil when
// td is not a pointer to an array.
func PtrArrayType(td *TypeDef) *ast.ArrayType {
	if td == nil {
		return nil
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	st, ok := x.(*ast.StarExpr)
	if !ok {
		return nil
	}
	if at, ok := st.X.(*ast.ArrayType); ok && at.Len != nil {
		return at
	}
	return nil
}

// ArrayTypedef reports whether td is a fixed-length array type. Arrays
// are represented as a [Slice] whose Typ is such a typedef.
func ArrayTypedef(td *TypeDef) bool { return arrayTypedef(td) }

// arrayTypedef reports whether a typedef is a fixed-size array — its
// underlying AST is an ArrayType carrying a length (slices have none).
func arrayTypedef(td *TypeDef) bool {
	if td == nil {
		return false
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	at, ok := x.(*ast.ArrayType)
	return ok && at.Len != nil
}

func typeTagOf(td *TypeDef) string {
	if td == nil {
		return "?"
	}
	if td.Name != "" {
		name := td.Name
		if td.Pkg != nil && td.Pkg.Path != "" {
			// a host-bound td's Name is already "pkgpath.Name"
			if !strings.HasPrefix(name, td.Pkg.Path+".") {
				name = td.Pkg.Path + "." + name
			}
		}
		if len(td.TParams) > 0 || len(td.OuterArgs) > 0 {
			// an instantiated generic is a different type per type
			// args — `T[struct{int}]` and `T[struct{int "x"}]` hash
			// distinct like Go. Outer args are part of a func-local
			// generic's identity too (the `X` of `type X int` inside
			// F[T] differs per instantiation).
			var sb strings.Builder
			sb.WriteString(name)
			sb.WriteByte('[')
			first := true
			for _, p := range td.TParams {
				if !first {
					sb.WriteByte(',')
				}
				first = false
				atd, _ := td.Binds[p].(*TypeDef)
				sb.WriteString(typeTagOf(atd))
			}
			for _, a := range td.OuterArgs {
				if !first {
					sb.WriteByte(',')
				}
				first = false
				atd, _ := a.(*TypeDef)
				sb.WriteString(typeTagOf(atd))
			}
			sb.WriteByte(']')
			name = sb.String()
		}
		return name
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	if x != nil {
		return anonTag(x)
	}
	if td.Kind == KindPointer && td.Elem != nil && td.Elem.HostIdent != nil {
		// the *T of a nil host pointer (vm.HostNilPtrTypedef) carries
		// no AST: spell it like the script's `*pkg.T` so a nil
		// *os.File from os.Open keys like a script (*os.File)(nil).
		return "*" + td.Elem.HostIdent.String()
	}
	return fmt.Sprintf("%p", td)
}

// anonTag renders an anonymous type for a canonical-key tag: the
// structural spelling keeps two separately-written `struct{A int}` keys
// equal, like Go's identical-type rule for unnamed types.
func anonTag(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + anonTag(t.X)
	case *ast.ArrayType:
		n := ""
		if t.Len != nil {
			switch l := t.Len.(type) {
			case *ast.BasicLit:
				n = l.Value
			case *ast.Ident:
				n = l.Name
			case *ast.Ellipsis:
				n = "..."
			}
		}
		return "[" + n + "]" + anonTag(t.Elt)
	case *ast.MapType:
		return "map[" + anonTag(t.Key) + "]" + anonTag(t.Value)
	case *ast.ChanType:
		return "chan " + anonTag(t.Value)
	case *ast.SelectorExpr:
		return anonTag(t.X) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return anonTag(t.X) + "[" + anonTag(t.Index) + "]"
	case *ast.IndexListExpr:
		var sb strings.Builder
		sb.WriteString(anonTag(t.X))
		sb.WriteByte('[')
		for i, ix := range t.Indices {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(anonTag(ix))
		}
		sb.WriteByte(']')
		return sb.String()
	case *ast.ParenExpr:
		return anonTag(t.X)
	case *ast.Ellipsis:
		return "[]" + anonTag(t.Elt)
	case *ast.StructType:
		var sb strings.Builder
		sb.WriteString("struct{")
		for i, f := range t.Fields.List {
			if i > 0 {
				sb.WriteByte(';')
			}
			for j, n := range f.Names {
				if j > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString(n.Name)
			}
			sb.WriteString(":" + anonTag(f.Type))
			if tag := structTagKey(f.Tag); tag != "" {
				sb.WriteString(" " + tag)
			}
		}
		sb.WriteString("}")
		return sb.String()
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func()"
	}
	return fmt.Sprintf("%T", e)
}

// Tuple holds the results of a call returning more than one value, in
// order.
type Tuple struct{ Elems []Value }

// Slice is a slice or array value; it is an array when [ArrayTypedef]
// reports true for Typ. Use [Slice.Len] and [Slice.Cap] rather than
// len(Elems): a slice of zero-size elements may be [Slice.Virtual].
type Slice struct {
	Elems []Value
	// N is the logical length of a virtual slice, whose zero-size
	// elements are not stored: make([]struct{}, n) allocates nothing for
	// any n, like Go's zerobase ($GOROOT/test/fixedbugs/issue29190.go).
	// Index reads yield Zero and element writes are unobservable (a
	// zero-size type has a single value). CapN is the logical capacity
	// and can exceed N — a cap-preserving reslice (s[:0]) or a
	// within-capacity append keeps it — so virtual-ness is a property
	// of the representation, not of N: N > 0 || CapN > 0.
	N, CapN int64
	Zero    Value
	// Typ is the declared slice type when one is known (a named literal or
	// a `var s S` bind): element stores re-coerce and named slice types
	// keep their identity on rebinds.
	Typ *TypeDef
}

// Virtual reports whether the slice holds zero-size elements that are not
// stored: Elems is empty while N and CapN carry the logical length and
// capacity. A virtual slice resliced to length and capacity 0 reports
// false; it is then an ordinary empty slice.
func (s *Slice) Virtual() bool { return s.N > 0 || s.CapN > 0 }

// Len reports the slice's length: N for a [Slice.Virtual] slice,
// len(Elems) otherwise.
func (s *Slice) Len() int64 {
	if s.Virtual() {
		return s.N
	}
	return int64(len(s.Elems))
}

// Cap reports the slice's capacity: CapN for a [Slice.Virtual] slice,
// cap(Elems) otherwise.
func (s *Slice) Cap() int64 {
	if s.Virtual() {
		return s.CapN
	}
	return int64(cap(s.Elems))
}

// Spread is the single argument passed for a trailing `xs...` when xs is
// a [Slice.Virtual] slice, which cannot expand into one argument per
// element. append and variadic-parameter binding expand it to the
// logical length; every other callee fails on it rather than answering
// wrong.
type Spread struct{ S *Slice }

// IsZeroSizeValue reports whether a value's type occupies no bytes — an
// empty struct, a [0]T array, or a struct of only zero-size fields. Go
// stores all such values at the same address.
func IsZeroSizeValue(v Value) bool {
	switch x := v.(type) {
	case *Named:
		return IsZeroSizeValue(x.V)
	case *Struct:
		for _, fv := range x.Fields {
			if !IsZeroSizeValue(fv) {
				return false
			}
		}
		return true
	case *Slice:
		// a zero-length value is only zero-size when its typedef says
		// array — an empty []T slice is a header, not zerobase.
		if len(x.Elems) != 0 {
			return false
		}
		td := x.Typ
		if td == nil {
			return false
		}
		a := td.Anon
		if a == nil && td.Spec != nil {
			a = td.Spec.Type
		}
		at, ok := a.(*ast.ArrayType)
		return ok && at.Len != nil
	}
	return false
}

// Map is a map value. Pairs is keyed by the [CanonicalKey] of each script
// key; Order and Keys hold the script key and canonical key of each entry
// in insertion order. Modify it through [Map.Insert], [Map.Delete] and
// [Map.Clear] to keep the three in sync, and iterate with [Map.Len] and
// [Map.At].
type Map struct {
	Pairs map[Value]Value
	Order []Value // insertion order for stable-ish range
	Keys  []Value // canonical key per Order slot — a NaN canonical key is
	// a fresh nonce on every CanonicalKey call, so iteration remembers
	// the one used at insert instead of recomputing it.
	Typ *TypeDef // declared map type (nil => missing keys yield NIL)
	// IfaceKey marks a map whose declared key type is an interface:
	// its keys hash by (dynamic type, value) ([IfaceKeyOf]). Set it
	// when the map is made, before any insert.
	IfaceKey bool
}

// canonicalKey is the key k stores under in m.
func (m *Map) canonicalKey(k Value) Value {
	if m.IfaceKey {
		return IfaceKeyOf(k)
	}
	return CanonicalKey(k)
}

// Len reports the number of stored pairs.
func (m *Map) Len() int {
	return len(m.Order)
}

// At returns the i-th entry in insertion order: the script key and its
// current value.
func (m *Map) At(i int) (Value, Value) {
	return m.Order[i], m.Pairs[m.Keys[i]]
}

// Get looks up the script key k. A NaN key is never found. To look up a
// key that is already canonical (from Pairs or [Map.SnapshotKeys]), use
// [Map.LookupCanonical].
func (m *Map) Get(k Value) (Value, bool) {
	v, ok := m.Pairs[m.canonicalKey(k)]
	return v, ok
}

// SnapshotKeys copies the script keys and their canonical keys, in
// insertion order, for iterating while the map changes. Check whether an
// entry is still present with [Map.LookupCanonical] on the canonical
// key; [Map.Get] on a NaN script key would never find it.
func (m *Map) SnapshotKeys() (display, canonical []Value) {
	return append([]Value(nil), m.Order...), append([]Value(nil), m.Keys...)
}

// LookupCanonical looks up a canonical key taken from Pairs or
// [Map.SnapshotKeys]. Look up script keys with [Map.Get].
func (m *Map) LookupCanonical(ck Value) (Value, bool) {
	v, ok := m.Pairs[ck]
	return v, ok
}

// Insert stores v under the script key k; a new key is appended to the
// insertion order. A NaN key always adds a new entry.
func (m *Map) Insert(k, v Value) {
	// Keeping canonicalization and the order/keys append in one place is
	// what makes the NaN-nonce key policy hold at every write site.
	ck := m.canonicalKey(k)
	if m.Pairs == nil {
		m.Pairs = map[Value]Value{}
	}
	if _, exists := m.Pairs[ck]; !exists {
		m.Order = append(m.Order, k)
		m.Keys = append(m.Keys, ck)
	}
	m.Pairs[ck] = v
}

// Delete removes the script key k and reports whether it was present. A
// NaN key is never present, as in Go.
func (m *Map) Delete(k Value) bool {
	// The canonical key stored per slot tells which order slot to drop —
	// a stale order entry would render and range as `k:<nil>`.
	ck := m.canonicalKey(k)
	if _, ok := m.Pairs[ck]; !ok {
		return false
	}
	delete(m.Pairs, ck)
	for i := range m.Order {
		if m.Keys[i] == ck {
			m.Order = append(m.Order[:i], m.Order[i+1:]...)
			m.Keys = append(m.Keys[:i], m.Keys[i+1:]...)
			break
		}
	}
	return true
}

// Clear removes every entry.
func (m *Map) Clear() {
	m.Pairs = map[Value]Value{}
	m.Order = nil
	m.Keys = nil
}

// Chan is a channel value backed by a Go channel of [Value]: sends and
// receives block as in Go, capacity is honored, and close wakes every
// blocked receiver. The interpreter's blocking operations also watch the
// script process's done channel, so an exiting process releases them;
// Go code blocking on C directly does not get that.
type Chan struct {
	C chan Value
	// Typ is the declared channel type when one is known (make or a
	// `var c C` bind): sends re-coerce to the element type.
	Typ *TypeDef
}

// SelArm is one case of a select statement being executed, pushed by
// OpSelArm on entry to the select: the
// reflect.SelectCase to poll, whether it sends, how many values a receive
// binds (NRecv), and the channel's element typedef for the zero value a
// closed channel yields.
type SelArm struct {
	Case  reflect.SelectCase
	Send  bool
	NRecv int
	ETyp  *TypeDef
	// Wakeable marks an arm whose channel is host-carried (a timer's C,
	// a retained callback feed): a select blocked on it can end without
	// a script goroutine, so it never counts toward deadlock.
	Wakeable bool
	// WakeChan is the arm's managed-channel id (the hchan pointer a
	// timer/ticker proxy feed registered), 0 for unmanaged channels. It
	// is recorded even on a dead managed channel — a later re-arm
	// (Timer.Reset) must lift the park back out of the asleep count.
	WakeChan uintptr
}

// Task is the handle of one script goroutine. Done closes when its call ends —
// normally, by panic (Err), or by process exit (Aborted) — and
// [Task.Wait] reports how it finished. Parent links follow the spawn
// tree so callers can detect that waiting would deadlock.
type Task struct {
	Done    chan struct{}
	Err     error
	Parent  *Task
	Aborted bool // finished by process exit rather than its own outcome
	// Result carries the call's return value to a joiner — set before
	// Done closes, so a Wait that returns also observes it.
	Result Value
}

// Finish records the task's outcome and releases waiters. Call it once.
func (t *Task) Finish(err error, aborted bool) {
	t.Err = err
	t.Aborted = aborted
	close(t.Done)
}

// Wait blocks until the task ends and returns its Err.
func (t *Task) Wait() error {
	<-t.Done
	return t.Err
}

// TypeDef describes a type at run time: a declared type, a predeclared
// basic type ([BasicTypedef]), or an anonymous composite such as []int
// (Name empty, shape in Anon). Typedefs are values themselves, used as
// type arguments and as the callee of a conversion. Compare them with
// [TypIdentical] rather than ==: distinct *TypeDef values can denote the
// same type.
type TypeDef struct {
	Pkg     *Package
	Name    string
	File    *syntax.File
	Spec    *ast.TypeSpec
	Kind    TypeKind
	Fields  []string             // struct field order
	FTags   map[string]string    // struct tags
	Anon    ast.Expr             // underlying type AST (non-struct named types)
	Methods map[string]*Function // lazily built method set

	TParams      []string         // generic type parameter names (type Foo[T any] ...)
	TConstraints []ast.Expr       // constraint expr per TParams entry (nil = unconstrained)
	Binds        map[string]Value // instantiation bindings: type params -> TypeDef args
	// OuterArgs holds the enclosing generic function's resolved type
	// arguments when a function-local generic type instantiates inside
	// one — Go makes them part of the closure type's identity and renders
	// them in reflect.Type.String as `pkg.T[outerArgs;ownArgs]`.
	OuterArgs []Value

	// Interfaces: MReqs are the directly declared method names; IEmbeds are
	// the embedded element expressions (io.Reader, ~int unions, ...). The
	// full required set is computed by the engine on demand.
	MReqs   []string
	IEmbeds []ast.Expr

	// Embedding: for each embedded struct field (no declared name),
	// EmbedSpecs[i] is its type AST and EmbedIdx[i] its index in Fields.
	// Resolved lazily into Embeds by the engine's TypeMethods/MethodsOf.
	EmbedSpecs []ast.Expr
	EmbedIdx   []int
	Embeds     []*TypeDef

	// LocalTypes maps the function-local `type` declarations visible where
	// this typedef was declared — embedded specs and other type refs that
	// name a local type resolve through it (package indexes don't see them).
	LocalTypes map[string]*TypeDef

	// Elem is the resolved element/pointee typedef when Anon cannot
	// express it — e.g. a pointer typedef synthesized from `&x` whose
	// pointee is known only as a runtime typedef. Nil means resolve
	// through Anon/Spec instead.
	Elem *TypeDef

	// HostNew, when set, constructs the zero of this type as a host Go
	// value (sync.Mutex, sync.WaitGroup, ...): Zero returns a *GoValue
	// instead of a *Struct so member access dispatches through the host
	// method set. Set only on typedefs of bound host packages.
	HostNew func() any

	// HostScalar, when set, marks a bound named basic whose payload is a
	// stdlib scalar (time.Duration): the raw host value is stored, not a
	// GoValue box — Tag converts same-kind plain values to its reflect
	// type, and member access dispatches through the host method set.
	HostScalar any

	// HostIdent, when set, is the named host type a typedef synthesized at
	// the host boundary stands for — identity only, unlike HostNew it
	// builds no zero. An unboxed host []ast.Stmt keeps its element's
	// identity here so the source-interpreted go/ast's own Stmt can be
	// recognized as the same type (see [HostNamedIs]).
	HostIdent reflect.Type

	// Local marks a typedef declared inside a function body: Go gives
	// every such declaration its own identity, so same-named local types
	// in two different functions are distinct types (unlike package-level
	// types, which dedupe by package path + name).
	Local bool

	// Gen is the decl's `·gen` index — gc numbers each non-alias
	// function-local type in package source order and spells it inside
	// an instantiation's arg list (`main.U[int;int]·3`). Zero for
	// package-level and alias decls. Assigned by index.Build.
	Gen int

	// inInstArgs marks a spelling context — DisplayName copies the
	// typedef carrying this flag so every name it reaches (via TypGoSpelling
	// ctx, elem recursions) spells inside an instantiation's arg list
	// and a local typedef carries its `·gen` suffix.
	inInstArgs bool

	// OuterSpell holds the enclosing generic function's resolved type
	// arguments as display context: every typedef produced inside a
	// generic function spells its embedded function-local type names
	// with them (`func(main.U[int;int]·3)` inside F[int]). Display only —
	// identity keeps its own OuterArgs on the local decl's clone, so a
	// composite like `func(int)` dedupes across instantiations the way
	// Go does.
	OuterSpell []Value

	// fieldTypes caches the engine's resolved field typedefs ([]*TypeDef,
	// see CachedFieldTypes). A copy that changes what resolution reads
	// (Binds, Pkg, File, LocalTypes) must call ResetCaches.
	fieldTypes atomic.Value
	// ifaceReqs and ifaceSigs cache an interface typedef's flattened
	// requirement names (map[string]bool) and signature shells
	// (map[string]*Function), see CachedIfaceReqs/CachedIfaceSigs.
	// Interface satisfaction consults both on every conversion to the
	// interface, and rebuilding them dominated allocation in
	// interface-heavy scripts (go/parser's ast.Expr/ast.Stmt values).
	ifaceReqs atomic.Value
	ifaceSigs atomic.Value
	// hostType caches reflect.TypeOf(HostNew()) (a reflect.Type): the
	// constructor's native type is fixed at bind time, while assert and
	// conversion paths recompute it — and its zero-value allocation —
	// once per comparison otherwise.
	hostType atomic.Value
}

// CachedFieldTypes returns the field typedefs stored by
// [TypeDef.SetFieldTypes]; ok is false when none were stored.
func (td *TypeDef) CachedFieldTypes() ([]*TypeDef, bool) {
	fts, ok := td.fieldTypes.Load().([]*TypeDef)
	return fts, ok
}

// SetFieldTypes caches td's resolved field typedefs. They depend only on
// td's own declaration context, so every reader of td can share them. A
// copy of td whose context differs must call [TypeDef.ResetCaches].
func (td *TypeDef) SetFieldTypes(fts []*TypeDef) { td.fieldTypes.Store(fts) }

// CachedIfaceReqs returns the requirement names stored by
// [TypeDef.SetIfaceReqs]; ok is false when none were stored.
func (td *TypeDef) CachedIfaceReqs() (map[string]bool, bool) {
	reqs, ok := td.ifaceReqs.Load().(map[string]bool)
	return reqs, ok
}

// SetIfaceReqs caches an interface typedef's flattened requirement
// names. Like the field typedefs they depend only on td's declaration
// context; callers must treat the shared map as read-only.
func (td *TypeDef) SetIfaceReqs(reqs map[string]bool) { td.ifaceReqs.Store(reqs) }

// CachedIfaceSigs returns the signature shells stored by
// [TypeDef.SetIfaceSigs]; ok is false when none were stored.
func (td *TypeDef) CachedIfaceSigs() (map[string]*Function, bool) {
	sigs, ok := td.ifaceSigs.Load().(map[string]*Function)
	return sigs, ok
}

// SetIfaceSigs caches an interface typedef's method signatures, as
// [Function] values without bodies (nil when it declares none). Callers
// must treat the map as read-only. Returning the same Functions each time
// lets [SigMemo] remember comparisons against them.
func (td *TypeDef) SetIfaceSigs(sigs map[string]*Function) { td.ifaceSigs.Store(sigs) }

// HostNamedScalar reports the payload of a boxed host value of a named
// basic type — go/token's Token on an inspect.AST node, io/fs's FileMode
// from the host-bound os — as the script value a source declaration of
// that type holds, plus the host type. The value stays boxed (its host
// methods and printing keep working); equality and map keys compare it
// against the source twin's values through this payload.
func HostNamedScalar(x Value) (Value, reflect.Type, bool) {
	gv, ok := x.(*GoValue)
	if !ok {
		return nil, nil, false
	}
	return HostNamedPayload(gv.V)
}

// HostNamedPayload is [HostNamedScalar] on an unboxed host value — the
// form a host helper's []any arguments carry.
func HostNamedPayload(hv any) (Value, reflect.Type, bool) {
	rv := reflect.ValueOf(hv)
	if !rv.IsValid() || rv.Type().Name() == "" || rv.Type().PkgPath() == "" {
		return nil, nil, false
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), rv.Type(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if rv.Uint() > math.MaxInt64 {
			return nil, nil, false
		}
		return int64(rv.Uint()), rv.Type(), true
	case reflect.String:
		return rv.String(), rv.Type(), true
	case reflect.Bool:
		return rv.Bool(), rv.Type(), true
	}
	return nil, nil, false
}

// HostNamedIs reports whether the host type rt is the type a source
// typedef declares: same package path and name. The printed spelling
// cannot decide it — inside go/ast `*BlockStmt` spells "*BlockStmt",
// and under `import goast "go/ast"` a script spells "*goast.BlockStmt",
// while the host value prints "*ast.BlockStmt" either way. Function-local
// types and generic instances never match a host type.
func HostNamedIs(rt reflect.Type, td *TypeDef) bool {
	if rt == nil || td == nil || td.Pkg == nil || td.Pkg.Path == "" || td.Name == "" || td.Local || len(td.Binds) > 0 {
		return false
	}
	return rt.PkgPath() == td.Pkg.Path && rt.Name() == td.Name
}

// HostTwinOf reports whether a value of td is a value of the host type
// rt: td is rt's source twin ([HostNamedIs]) or the bound typedef that
// boxes rt as a scalar (time.Month's HostScalar). A boxed host named
// basic meets exactly these typedefs' values, never a look-alike of
// another type with the same payload.
func HostTwinOf(rt reflect.Type, td *TypeDef) bool {
	if rt == nil || td == nil {
		return false
	}
	if td.HostScalar != nil {
		return reflect.TypeOf(td.HostScalar) == rt
	}
	return HostNamedIs(rt, td)
}

// HostType returns reflect.TypeOf(td.HostNew()) without allocating a new
// zero value on every call. td.HostNew must be set.
func (td *TypeDef) HostType() reflect.Type {
	if rt, ok := td.hostType.Load().(reflect.Type); ok {
		return rt
	}
	rt := reflect.TypeOf(td.HostNew())
	if rt != nil {
		td.hostType.Store(rt)
	}
	return rt
}

// ResetCaches drops the cached field types, interface requirements and
// host type. Call it on a shallow copy whose Binds, Pkg, File or
// LocalTypes differ from the original's.
func (td *TypeDef) ResetCaches() {
	td.fieldTypes = atomic.Value{}
	td.ifaceReqs = atomic.Value{}
	td.ifaceSigs = atomic.Value{}
	td.hostType = atomic.Value{}
}

// TypeKind classifies a [TypeDef] by its underlying type. Predeclared
// basic types and types defined over them are [KindNamedBasic]; arrays
// are [KindSlice] (see [ArrayTypedef]).
type TypeKind uint8

const (
	KindStruct     TypeKind = iota
	KindNamedBasic          // type MyInt int etc.
	KindSlice
	KindMap
	KindFunc
	KindInterface
	KindAlias
	KindChan
	KindPointer // *T — a named or anonymous pointer type
)

// Struct is a struct value; Fields holds one value per name in
// Def.Fields, in the same order. Fields of a [Zero] struct are [NIL].
type Struct struct {
	Def    *TypeDef
	Fields []Value
}

// BoundMethod is a method value `x.M`: the method Fn bound to the
// receiver Recv.
type BoundMethod struct {
	Recv Value // *Cell for pointer receivers, plain Value otherwise
	Fn   *Function
	// Direct marks a method value bound in callee position (`i.M()` and
	// the `defer`/`go` variants): gc dispatches the promoted *T→T
	// wrapper directly, so a nil *T receiver panics as a plain nil
	// dereference — a lazily bound value (`f := i.M`) instead reports
	// "value method ... called using nil" when the call runs.
	Direct bool
}

// BuiltinFunc is a function implemented in Go, such as len or a member of
// a bound host package. Fn receives the arguments as script values and returns one [Value], a [*Tuple] for several results.
// An error returned from Fn stops the script with a [Trap] that script
// code cannot recover; to raise a recoverable script panic, panic with a
// [*Panic] such as [RuntimePanic]. Use the [VMCaller] to call back into
// script code.
type BuiltinFunc struct {
	Name string
	Fn   func(vm VMCaller, args []Value) (Value, error)
	// Pkg is the package this builtin was bound under (stamped by
	// Engine.Bind); nil for ad-hoc builtins.
	Pkg *Package
	// Target optionally holds the underlying Go func value the builtin
	// adapts — inspect.Signature reads reflect.TypeOf it for the real
	// signature. Nil for intrinsics that declare no target.
	Target any
	// Method optionally holds the reflected method this builtin adapts
	// when it was created for member access on a host value — inspect
	// reads it for the owner (receiver package), signature, and
	// definition position. Nil for plain builtins.
	Method *reflect.Method
	// GenFn, when set, makes this a generic builtin: instantiating it
	// (F[T]) produces a plain BuiltinFunc that calls GenFn with the
	// bound type arguments (e.g. reflect.TypeFor[T]).
	GenFn func(vm VMCaller, targs []Value, args []Value) (Value, error)
	// SyncCallbacks declares that Fn calls back into the VM only
	// synchronously — on the calling goroutine, before Fn returns —
	// and never retains the VMCaller (no stored callbacks, iterators or
	// goroutines). The VM can then skip its cross-goroutine check.
	SyncCallbacks bool
}

// SmallIntOf converts a host value of any integer kind, including named
// ones like time.Duration or reflect.Kind, to int64. ok is false for
// non-integers and for unsigned values above math.MaxInt64; uint64 is
// never converted.
func SmallIntOf(x any) (int64, bool) {
	rv := reflect.ValueOf(x)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uintptr:
		if u := rv.Uint(); u <= math.MaxInt64 {
			return int64(u), true
		}
	}
	return 0, false
}

// VMCaller is the interpreter as seen by a [BuiltinFunc]: it calls script
// functions and answers questions about types that only the running
// engine can resolve.
type VMCaller interface {
	// Call calls a callable script value — a function, closure, method
	// value, builtin, or a typedef (a conversion).
	Call(fn Value, args []Value) (Value, error)
	// Recover implements the recover() builtin: it returns the in-flight
	// panic value when called directly by a deferred function, else nil.
	Recover() Value
	// Member selects base.name for a host intrinsic (e.g. calling a
	// script-defined Unwrap on an error value); ok=false when the member
	// does not exist or selection traps.
	Member(base Value, name string) (m Value, ok bool)
	// MethodSetOf returns the names in v's dynamic method set under Go's
	// receiver rule — a value's set excludes pointer receivers while a
	// pointer's includes them — so host intrinsics answering "does v
	// implement X" (fmt's Stringer probe, io.Reader adapters) apply the
	// same rule the VM's interface checks do. unsure=true means embedded
	// types failed to resolve and the set may be incomplete; nil set
	// means the engine offers no method-set hook.
	MethodSetOf(v Value) (set map[string]bool, unsure bool)
	// MapKeyOperand applies the declared-key-type check `m[k]` runs and
	// returns the unwrapped operand — builtin delete uses it so deleting
	// coerces the key the same way indexing does (map[float64]int{1:7}
	// deletes by float64(1), not int64(1)).
	MapKeyOperand(mapTd *TypeDef, key Value) Value
	// NewMap returns an empty map of the map typedef td, keyed the way
	// its declared key type keys in Go ([Map.IfaceKey]) — used by make.
	NewMap(td *TypeDef) *Map
	// Equal reports a == b for two values of type td, as an element
	// comparison of a []td: an interface td compares (dynamic type,
	// value) pairs, a boxed host named basic meets its source twin.
	// Convert an untyped operand to td first.
	Equal(td *TypeDef, a, b Value) bool
	// Zero returns the Go zero value of a typedef (struct fields get
	// typed zeros, nilable kinds get TypedNil) — used by new().
	Zero(td *TypeDef) Value
	// ElemZero returns the element-type zero of a container typedef —
	// for a slice/map/pointer/chan td, the zero of its element; NIL when
	// the element type is unknown. Lets intrinsics recover an element
	// typedef via TypeOf(ElemZero(td)).
	ElemZero(td *TypeDef) Value
	// Package returns the package of the innermost running frame — the
	// caller's package for inspect.Current. Nil when no frame runs.
	Package() *Package
	// TypeOf returns the typedef describing a runtime value — used by
	// new(expr) to type the allocated cell.
	TypeOf(x Value) *TypeDef
	// Convert implements the conversion T(x) on the calling goroutine —
	// what Call(td, x) does, without the engine-boundary bookkeeping
	// (re-entry check, panic capture) a conversion never needs.
	Convert(td *TypeDef, x Value) (Value, error)
	// Copy returns a copy of v following Go assignment semantics
	// (structs copy, slices/maps/pointers share) — used by new(expr).
	// Unlike the package-level Copy it also materializes an untyped
	// constant to a concrete value.
	Copy(x Value) Value
	// Spawn runs fn(args) on a new script goroutine, as the `go`
	// statement does. A panic in it fails the whole script, unless the
	// script is already exiting.
	Spawn(fn Value, args []Value) *Task
	// Task returns the handle of the calling goroutine — nil on the
	// root goroutine.
	Task() *Task
	// ArrayLenOf reports the length of an array typedef, evaluating a
	// constant length expression ([3]int, [N]int, [N*2]int); ok=false
	// for non-array types.
	ArrayLenOf(td *TypeDef) (n int64, ok bool)
	// ResolveType resolves a type expression in the context of typedef
	// `from` (its package scope, file imports and generic binds), for
	// example an embedded field's type.
	ResolveType(from *TypeDef, x ast.Expr) (*TypeDef, error)
	// FieldTypes returns the declared typedef of each struct field,
	// parallel to td.Fields; an entry is nil when its declaration does
	// not resolve.
	FieldTypes(td *TypeDef) ([]*TypeDef, error)
	// ElemOf returns the element typedef of a container typedef
	// ([]T->T, map[K]V->V, chan T->T) or the pointee typedef of a
	// pointer typedef — named pointer types included.
	ElemOf(td *TypeDef) (*TypeDef, error)
	// CallerPCs returns opaque handles for the script call stack —
	// live frames plus frames already unwound by the in-flight panic,
	// innermost first like runtime.Callers. Resolve them with
	// CallerFrame.
	CallerPCs() []uintptr
	// CallerFrame resolves a handle from CallerPCs to its call site.
	CallerFrame(pc uintptr) (site CallSite, ok bool)
}

// CallSite is one script call-stack entry: the function's Go-style symbol name (as runtime.Func.Name reports it)
// and the source position it is executing.
type CallSite struct {
	Name string
	File string
	Line int
}

// Function is a script function, method or function literal, compiled
// on first call through Compile ([Function.EnsureCompiled]). As a value,
// a function literal that captures variables is a [Closure] over its
// Function (a capture-free literal is the *Function itself), a method
// value `x.M` is a [BoundMethod], and a function implemented in Go is a
// [BuiltinFunc]. A generic function is instantiated with
// [Function.WithBinds].
type Function struct {
	Pkg     *Package
	File    *syntax.File
	Decl    *ast.FuncDecl // nil for the synthetic package __init__
	Name    string
	Recv    string // receiver type name, "" for plain funcs
	PtrRecv bool

	TParams      []string         // generic type parameter names
	TConstraints []ast.Expr       // constraint expr per TParams entry
	Binds        map[string]Value // compile-time bindings: type params -> TypeDef args

	// OuterTParams names the enclosing generic instantiation's type
	// parameters a function literal closes over — set by OpMakeClosure
	// when the enclosing function's binds merge into the literal, so a
	// local type declared in the body still differs per instantiation.
	OuterTParams []string

	Compile func(*Function) error // injected by the engine
	once    sync.Once
	cerr    error
	Chunk   *bytecode.Chunk
}

// EnsureCompiled compiles the function once, through Compile, and
// returns the compile error; later calls return the same error. A nil
// Compile leaves Chunk as it is.
func (f *Function) EnsureCompiled() error {
	f.once.Do(func() {
		if f.Compile != nil {
			f.cerr = f.Compile(f)
		}
	})
	return f.cerr
}

// WithBinds returns a copy of f with the type-parameter bindings binds
// (type parameter name to [*TypeDef]). The copy shares f's compiled code,
// compiling f first if needed, and never recompiles.
func (f *Function) WithBinds(binds map[string]Value) *Function {
	// A field-wise copy instead of `*f` keeps the sync.Once uncopied.
	f.EnsureCompiled()
	cp := &Function{
		Pkg:          f.Pkg,
		File:         f.File,
		Decl:         f.Decl,
		Name:         f.Name,
		Recv:         f.Recv,
		PtrRecv:      f.PtrRecv,
		TParams:      f.TParams,
		TConstraints: f.TConstraints,
		Binds:        binds,
		OuterTParams: f.OuterTParams,
		Compile:      f.Compile,
		cerr:         f.cerr,
		Chunk:        f.Chunk,
	}
	cp.once.Do(func() {})
	return cp
}

// OuterParamNames lists the type-parameter names visible inside f: those
// of the enclosing generic function (OuterTParams) first, then f's own.
func (f *Function) OuterParamNames() []string {
	if len(f.OuterTParams) == 0 {
		return f.TParams
	}
	names := make([]string, 0, len(f.OuterTParams)+len(f.TParams))
	names = append(names, f.OuterTParams...)
	names = append(names, f.TParams...)
	return names
}

// Closure is a function literal value: Fn plus the [Cell] of each
// variable it captures.
type Closure struct {
	Fn     *Function
	Upvals []*Cell
}

// Iterator is the state of a range loop in progress.
type Iterator struct {
	Kind   byte // 's' slice, 'm' map, 'i' int, 'x' string, 'c' chan, 'f' func
	Elems  []Value
	Keys   []Value // map keys
	Idx    int
	Limit  int // for integer ranges
	String string
	// M is the map an 'm' iterator ranges over. Elems/Keys snapshot the
	// display/canonical keys at iterator creation, but each pair is
	// looked up live — an entry deleted before it is reached is skipped
	// (Go: "if a map entry that has not yet been reached is removed
	// during iteration, the corresponding iteration value will not be
	// produced"), and a value written before the reach reads current.
	M *Map
	// NilArr marks an 'i' iterator walking the indices of a nil *[N]T —
	// the index sequence is legal Go but reading an element derefs nil.
	NilArr bool
	// Zero marks an 's' iterator over a virtual zero-size-element slice —
	// Limit counts the elements and each pair vends this shared value.
	Zero Value
	// ChRV is the reflect channel a channel range receives from; ETyp is
	// its element typedef for closed-receive zero values.
	// HostChan marks a host-carried channel (a timer's C): blocking on it
	// can end without a script goroutine, so it never counts toward
	// deadlock.
	ChRV     reflect.Value
	ETyp     *TypeDef
	HostChan bool

	// Fn is the producer for 'f' (range-over-func) iterators. Started marks
	// that the producer was invoked once; Exited marks that the loop body
	// abandoned the loop (break/goto/return) or died on panic, after which
	// further yield calls must panic.
	Fn      Value
	Started bool
	Exited  bool
}

// GoValue boxes a Go value V that has no script representation of its
// own, such as a host object or a complex number. Scripts can call V's
// methods.
type GoValue struct{ V any }

// UConst is an untyped constant kept at arbitrary precision: a rune
// literal (default type rune, not int), an integer that does not fit
// int64, a float literal overflowing float64, or a complex constant.
// Conversion, assignment, arithmetic and calls convert it to a concrete
// value as Go would, so `const B = 1<<100` works while `var x = 1<<100`
// fails. [UConstNative] converts it to its default type.
type UConst struct {
	V    constant.Value
	Rune bool // Kind()==Int but the literal/expression is rune-flavored

	// Wrap marks the result of a NON-constant shift whose left operand
	// was an untyped constant (`1 << n` with a variable n). Go types
	// that operand from the context — the other operand, the assigned
	// variable, or int by default — and shifts at that width, so the
	// value is not a constant: materializing it into an integer type
	// truncates to the type's width (two's complement) instead of
	// reporting "constant overflows". V holds the result reduced mod
	// 2^64 into int64 range, which agrees with every width's truncation
	// for the modular operations (+ - * & | ^ &^ <<) the VM keeps
	// folding it through — and stays bounded for `1 << (1<<40)`.
	Wrap bool
	// WrapSure marks a Wrap value whose count the compiler proved
	// non-constant (a variable, a call, a field read). Only then is
	// materializing it into a float, complex or string type gc's
	// "shifted operand must be integer" error: an unproven count may
	// still be a constant the compiler's walk could not resolve.
	WrapSure bool

	// memo caches the last operand-typed conversion (see Memo): a chunk
	// constant is one shared *UConst, re-adapted on every execution of
	// `r == ' '`.
	memo atomic.Pointer[constMemo]
}

type constMemo struct {
	key any
	v   Value
}

// Memo returns the value [UConst.SetMemo] stored under key. Callers key
// by what determines the conversion (the operand's typedef, or its scalar
// kind) and must only store immutable values: hits share one value.
func (u *UConst) Memo(key any) (Value, bool) {
	if m := u.memo.Load(); m != nil && m.key == key {
		return m.v, true
	}
	return nil, false
}

// SetMemo caches v under key, replacing any previous entry: only the
// most recent key is remembered.
func (u *UConst) SetMemo(key any, v Value) { u.memo.Store(&constMemo{key: key, v: v}) }

// DefaultName returns the name of the constant's default type: "bool",
// "string", "int", "int32" (for a rune), "float64" or "complex128".
func (u *UConst) DefaultName() string {
	switch u.V.Kind() {
	case constant.Bool:
		return "bool"
	case constant.String:
		return "string"
	case constant.Int:
		if u.Rune {
			return "int32"
		}
		return "int"
	case constant.Float:
		return "float64"
	case constant.Complex:
		return "complex128"
	}
	return "unknown"
}

// SymbolID names a package-level declaration by the import path of the
// package that declares it, whatever alias the calling file imports it
// under. It is the key of a special form: see [SpecialFunc] and
// [SpecialContext.ResolveSymbol].
type SymbolID struct {
	PackagePath string
	Name        string
}

// QuotedCall is the call a [SpecialFunc] handles: Call is the call
// expression with its unevaluated arguments, in File. Evaluate parts of it
// on demand with [SpecialContext.Eval] and related methods; Locals and
// Upvals record the caller's variables they can see.
type QuotedCall struct {
	Call   *ast.CallExpr
	File   *syntax.File
	Locals map[string]int // visible local name -> caller slot index
	Upvals map[string]int // visible upvalue name -> caller upvalue index
}

// SpecialFunc handles calls to a special form, registered with
// [github.com/podhmo/minigo.Engine.RegisterSpecial]. It runs each time
// execution reaches such a call, receiving the call unevaluated as a
// [QuotedCall]; its result is the call's value. A returned error stops
// the script with a [Trap] that wraps it.
type SpecialFunc func(ctx SpecialContext, call *QuotedCall) (Value, error)

// SpecialContext is the interpreter as seen by a [SpecialFunc]: source
// positions and on-demand evaluation of the quoted expressions in the
// caller's scope.
type SpecialContext interface {
	// Position resolves an AST node's source position in the caller's file set.
	Position(n ast.Node) token.Position
	// File is the source file containing the special call.
	File() *syntax.File
	// Package is the package containing the special call.
	Package() *Package
	// Eval evaluates expr in the caller's scope (locals, upvalues,
	// package globals, imports, builtins).
	Eval(expr ast.Expr) (Value, error)
	// Call invokes a callable runtime value.
	Call(fn Value, args []Value) (Value, error)
	// ResolveSymbol resolves expr to a canonical symbol identity without
	// evaluating or initializing anything: pkg.Sym maps through the caller
	// file's import table to SymbolID{package path, name}; a bare
	// identifier maps to a member of the caller's package. Local/upvalue
	// names and non-symbol expressions are an error. This is the index-
	// level laziness special forms exploit: quoting huge.ConvertFoo
	// yields its SymbolID without initializing huge.
	ResolveSymbol(expr ast.Expr) (SymbolID, error)
	// Resolve resolves a symbol expression (a bare identifier or a
	// pkg.Sym selector on an import alias) to its runtime value. Unlike
	// Eval it accepts no arbitrary expression: resolution goes through
	// normal name lookup, so a package is located/indexed lazily and only
	// the named declaration is materialized (var/const members do run the
	// package init, via Member's EnsureReady). Local/upvalue names
	// resolve to their current cell contents; other expressions are an
	// error.
	Resolve(expr ast.Expr) (Value, error)
	// ResolveType resolves a type expression to its runtime typedef.
	// Named types go through Resolve (package decls materialize to the
	// TypeDef itself; builtin names like int resolve to their typedefs);
	// composite types ([]T, map[K]V, *T, chan T, struct{...},
	// interface{...}, func signatures) yield anonymous typedefs carrying
	// Anon specs so element types resolve lazily on use — the same shape
	// the compiler emits for declared types. T[Args] instantiates a
	// generic typedef with the argument typedefs.
	ResolveType(expr ast.Expr) (*TypeDef, error)
	// Format renders an AST node back to source text.
	Format(n ast.Node) string
	// Errorf reports a failure attributed to an AST node.
	Errorf(n ast.Node, format string, args ...any) error
}

// maxTracebackEntries bounds how many frames Error() renders; the full
// list stays in Frames for programmatic use, but a runaway recursion
// shouldn't dump tens of thousands of lines. Long traces keep both ends —
// the innermost frames where the failure happened and the outermost entry
// points — with the middle elided.
const maxTracebackEntries = 1000

func renderFrames(frames []string) string {
	frames = foldRepeatedFrames(frames)
	if len(frames) <= maxTracebackEntries {
		return strings.Join(frames, "\n")
	}
	half := maxTracebackEntries / 2
	return strings.Join(frames[:half], "\n") +
		fmt.Sprintf("\n... %d frames elided ...\n", len(frames)-maxTracebackEntries) +
		strings.Join(frames[len(frames)-half:], "\n")
}

// maxRunShown bounds how many entries of one repeated run render
// verbatim before folding: a stack trace with ~50 real frames is still
// debuggable, while a runaway recursion's thousands are not.
const maxRunShown = 50

// foldRepeatedFrames collapses long runs of the same entry into
// "<entry> x50\n... repeated N more times ..." — a bounded-verbosity
// version of the fold CPython does for a RecursionError. A
// stack-exhausted recursion otherwise renders the same frame hundreds of
// times, which is noise for a human and context poison for an agent;
// keeping the run's first maxRunShown preserves the depth feel. Folding
// runs before the head/tail cap so the cap measures the folded list.
func foldRepeatedFrames(frames []string) []string {
	out := make([]string, 0, len(frames))
	for i := 0; i < len(frames); {
		j := i + 1
		for j < len(frames) && frames[j] == frames[i] {
			j++
		}
		if n := j - i; n > maxRunShown {
			out = append(out, frames[i:i+maxRunShown]...)
			out = append(out, fmt.Sprintf("... repeated %d more times ...", n-maxRunShown))
		} else {
			out = append(out, frames[i:j]...)
		}
		i = j
	}
	return out
}

// Panic is a script panic in flight. Unlike a [Trap], script code can
// recover() it. Go code such as a [BuiltinFunc]
// raises one by calling panic with it (see [RuntimePanic] and
// [PlainPanic]). Value is the argument to panic, or a payload such as
// [*RuntimeError] for a run-time error. An unrecovered panic reaches the
// host as the error of [github.com/podhmo/minigo.Engine.Call] (find it
// with errors.As); its Error method prints it as Go would, with a
// traceback.
type Panic struct {
	Value   Value
	Frames  []string // "func at file:line" entries collected while unwinding
	GoStack string   // host goroutine stack at panic time (host panics only)
	// VC is the caller a script payload's Error()/String() runs through
	// when Error() renders `panic: <v>` — set by the panic builtin so
	// `panic(err)` prints the method's text instead of a value dump,
	// and consulted for every element of a superseded chain. Nil for
	// host-payload panics, which render natively.
	VC VMCaller
	// Prev is the panic this one superseded — a panic raised while
	// another unwinds links back to it; Go prints the chain
	// oldest-first (`panic: old` / `\tpanic: new`), rendering only the
	// head's traceback.
	Prev *Panic
	// Recovered marks a panic recover() consumed — the chain renders
	// it with gc's ` [recovered]` marker.
	Recovered bool
}

func (p *Panic) Error() string {
	var chain []*Panic
	for pp := p; pp != nil; pp = pp.Prev {
		chain = append(chain, pp)
	}
	var sb strings.Builder
	for i := len(chain) - 1; i >= 0; i-- {
		if i < len(chain)-1 {
			sb.WriteString("\n\t")
		}
		pv := panicValue(chain[i].Value, chain[i].VC)
		if pf, fatal := pv.(*panicPrintFatal); fatal {
			// a panic inside the payload's own Error()/String() probe is
			// itself fatal in gc (panic inside printpanics): nothing of
			// the `panic: ...` line prints — only the fatal message and
			// the dying method's frames.
			s := fmt.Sprintf("fatal error: panic while printing panic value: %s", pf.describe(chain[i].VC))
			if len(pf.inner.Frames) > 0 {
				s += "\nTraceback (most recent call first):\n" + renderFrames(pf.inner.Frames)
			}
			return s
		}
		fmt.Fprintf(&sb, "panic: %v", pv)
		if chain[i].Recovered {
			sb.WriteString(" [recovered]")
		}
	}
	s := sb.String()
	if len(p.Frames) > 0 {
		s += "\nTraceback (most recent call first):\n" + renderFrames(p.Frames)
	}
	if p.GoStack != "" {
		s += "\n" + p.GoStack
	}
	return s
}

// panicPrintFatal marks a panic payload whose Error/String probe
// panicked — gc's printpanics dies `fatal error: panic while printing
// panic value` on exactly this. inner is the panic the probe call
// raised; its frames point inside the method.
type panicPrintFatal struct {
	inner *Panic
	name  string
}

// describe spells the inner panic on gc's fatal line — `type <T>` for
// every payload but a bare string, which prints its own text
// (`panic while printing panic value: inner-str`).
func (pf *panicPrintFatal) describe(c VMCaller) string {
	v := pf.inner.Value
	if uc, ok := v.(*UConst); ok {
		if nv, err := UConstNative(uc); err == nil {
			v = nv
		}
	}
	if s, ok := v.(string); ok {
		return s
	}
	return "type " + pf.typeName(c)
}

// typeName spells the inner panic's type the way gc's fatal line does
// (`type runtime.errorString`): the payload's static type — host error
// payloads name their Go runtime counterparts, script values their
// declared type.
func (pf *panicPrintFatal) typeName(c VMCaller) string {
	switch re := pf.inner.Value.(type) {
	case *RuntimeError:
		if re.Kind != "" {
			return "runtime." + re.Kind
		}
		return "runtime.errorString"
	case PlainError:
		return "runtime.plainError"
	case *TypeAssertionError:
		return "*runtime.TypeAssertionError"
	case *PanicNilError:
		return "*runtime.PanicNilError"
	}
	if c != nil {
		if td := c.TypeOf(pf.inner.Value); td != nil {
			return DisplayName(td)
		}
	}
	if gv, ok := pf.inner.Value.(*GoValue); ok {
		return fmt.Sprintf("%T", gv.V)
	}
	return fmt.Sprintf("%T", pf.inner.Value)
}

// RuntimeError is the [Panic] payload of a run-time error (index out of
// range, nil dereference, ...): a script's recover() sees it as an error
// value implementing Go's runtime.Error. Kind is the name of the Go
// runtime type it stands for, such as "boundsError"; empty means
// "errorString". Its Error text has the "runtime error: " prefix;
// [PlainError] is the payload for run-time errors Go reports without it.
type RuntimeError struct {
	Msg  string
	Kind string
}

func (e *RuntimeError) Error() string { return "runtime error: " + e.Msg }

// RuntimeError marks the payload as a runtime.Error (Go's interface
// requires both Error and RuntimeError).
func (*RuntimeError) RuntimeError() {}

// PanicNilError is the [Panic] payload recover() sees for panic(nil),
// matching Go's *runtime.PanicNilError.
type PanicNilError struct{}

func (*PanicNilError) Error() string {
	return "runtime error: panic called with nil argument"
}

// panicValue renders the panic payload the way gc's printpanics does:
// a boxed host value unwraps so fmt sees the native; a script payload
// offering Error/String prints the method's text (error first, like
// gc's probe order); any other composite prints `(type) 0xADDR`; a
// named scalar prints `pkg.T(v)` — except a predeclared name, whose
// payload prints bare. c may be nil — without an engine the method
// probes and type spelling are skipped and the value renders as %v.
func panicValue(v Value, c VMCaller) any {
	switch t := v.(type) {
	case *GoValue:
		return t.V
	case *Named:
		// a host-boxed payload renders natively regardless of the
		// interface tag it carries (e.g. `var e error = errors.New`).
		if gv, ok := t.V.(*GoValue); ok {
			return gv.V
		}
	}
	if c == nil {
		return v
	}
	if s, ok, inner := ifaceCallStringFatal(c, v, "Error"); inner != nil {
		return &panicPrintFatal{inner: inner, name: "Error"}
	} else if ok {
		return s
	}
	if s, ok, inner := ifaceCallStringFatal(c, v, "String"); inner != nil {
		return &panicPrintFatal{inner: inner, name: "String"}
	} else if ok {
		return s
	}
	if _, isErr := v.(error); isErr {
		// host error payloads (RuntimeError, PanicNilError, ...)
		// keep fmt's %v -> Error() path.
		return v
	}
	switch t := v.(type) {
	case *Named:
		if panicComposite(t.V) {
			return fmt.Sprintf("(%s) %p", DisplayName(t.Typ), v)
		}
		if t.Typ != nil && (t.Typ.Pkg != nil || t.Typ.Spec != nil) {
			return fmt.Sprintf("%s(%v)", DisplayName(t.Typ), panicScalar(t.V))
		}
		// a predeclared tag (int, byte, ...) prints the payload bare.
		return panicScalar(t.V)
	case *UConst:
		if nv, err := UConstNative(t); err == nil {
			return nv
		}
	case *TypedNil:
		if t.Typ != nil {
			switch t.Typ.Kind {
			// gc prints the pointer's own value for nilable pointer
			// kinds (nil -> 0x0) but the arg's address for a nil
			// slice, which is never zero.
			case KindPointer, KindChan, KindMap, KindFunc:
				return fmt.Sprintf("(%s) 0x0", DisplayName(t.Typ))
			default:
				return fmt.Sprintf("(%s) %p", DisplayName(t.Typ), v)
			}
		}
	default:
		if panicComposite(v) {
			if td := c.TypeOf(v); td != nil {
				return fmt.Sprintf("(%s) %p", DisplayName(td), v)
			}
		}
	}
	return v
}

// panicComposite reports whether a panic payload renders as
// `(type) 0xADDR` — gc's printany gives every non-scalar, non-method
// payload that form instead of dumping fields.
func panicComposite(v Value) bool {
	switch t := v.(type) {
	case *Named:
		return panicComposite(t.V)
	case *Struct, *Slice, *Map, *Chan,
		*Cell, *FieldRef, *IndexRef, *DerefRef,
		*Function, *Closure, *BoundMethod, *BuiltinFunc,
		*Tuple, *Iterator, *Task, *ImportRef, *Package, *TypeDef,
		*SymbolID, *CallSite, *Spread, *SelArm:
		return true
	}
	return false
}

// panicScalar renders a named scalar's payload inside `pkg.T(...)` the
// way gc's printany does: strings quote, other scalars print %v.
func panicScalar(v Value) any {
	if u, ok := v.(*UConst); ok {
		if nv, err := UConstNative(u); err == nil {
			v = nv
		}
	}
	if s, ok := v.(string); ok {
		return fmt.Sprintf("%q", s)
	}
	return v
}

// Trap is a failure the script cannot recover(), unlike a [Panic]: an
// unsupported construct, an invalid operation, or an error returned by a
// [BuiltinFunc] or [SpecialFunc]. Deferred calls run as it unwinds, but
// recover() in them returns nil and the Trap continues. It unwinds to the
// host as the error of [github.com/podhmo/minigo.Engine.Call]; Err,
// reachable with errors.As, is the host error it was raised from.
type Trap struct {
	Pos    token.Pos
	Reason string
	Frames []string // "func at file:line" entries collected while unwinding
	// Err is the host error the trap was raised from (a special-form
	// handler, builtin or host call that returned an error), or nil for
	// a trap the VM raised itself. Reason is its text; keeping the value
	// lets hosts errors.As their own typed errors through the trap.
	Err error
}

// Unwrap exposes the host error a trap was raised from.
func (t *Trap) Unwrap() error { return t.Err }

func (t *Trap) Error() string {
	if len(t.Frames) == 0 {
		return fmt.Sprintf("runtime trap: %s", t.Reason)
	}
	return fmt.Sprintf("runtime trap: %s\nTraceback (most recent call first):\n%s", t.Reason, renderFrames(t.Frames))
}

// StructFieldTags returns the unquoted tag of each field of st that has
// one, keyed by field name (an embedded field by its type name), or nil
// when no field has a tag. It is the form [TypeDef.FTags] holds.
func StructFieldTags(st *ast.StructType) map[string]string {
	var out map[string]string
	for _, f := range st.Fields.List {
		if f.Tag == nil {
			continue
		}
		tag, err := strconv.Unquote(f.Tag.Value)
		if err != nil {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		if len(f.Names) == 0 {
			// an embedded field is keyed by its type name: reflect
			// reports `inner.Conf `yaml:",inline"“ as field Conf.
			out[AnonFieldName(f.Type)] = tag
			continue
		}
		for _, n := range f.Names {
			out[n.Name] = tag
		}
	}
	return out
}

// IfaceMember selects the member name of v only if v's method set
// contains it, as an interface check would: a pointer-receiver method is
// absent from a non-pointer value's set. Use it to ask whether v
// implements an interface (a Stringer, an io.Reader) before calling the
// method. When the method set is unknown or incomplete
// ([VMCaller.MethodSetOf]), it falls back to [VMCaller.Member].
func IfaceMember(c VMCaller, v Value, name string) (Value, bool) {
	if set, unsure := c.MethodSetOf(v); set != nil && !set[name] && !unsure {
		return nil, false
	}
	return c.Member(v, name)
}

// ifaceCallStringFatal is IfaceCallString for the panic-value probes: a
// script panic raised inside the method call surfaces as inner (gc's
// printpanics treats a panic during the Error/String probe as fatal),
// while every other failure reports (s, ok) the lenient way.
func ifaceCallStringFatal(c VMCaller, v Value, name string) (s string, ok bool, inner *Panic) {
	defer func() {
		if r := recover(); r != nil {
			if p, isPanic := r.(*Panic); isPanic {
				inner = p
			}
			s, ok = "", false
		}
	}()
	m, ok := IfaceMember(c, v, name)
	if !ok || !isStringerMember(m) {
		return "", false, nil
	}
	r, err := c.Call(m, nil)
	if err != nil {
		// a script panic the method raised crosses the Call boundary as
		// an error wrapping *Panic — distinguish it from an ordinary call
		// failure (trap, missing arg) so the probe can report it as the
		// method panicking, not as the method being absent.
		var p *Panic
		if errors.As(err, &p) {
			return "", false, p
		}
		return "", false, nil
	}
	s, ok = r.(string)
	return s, ok, nil
}

// IfaceCallStringPanic is [IfaceCallString] that also returns the script
// panic the method raised, so the caller can report it as fmt does
// (`%!v(PANIC=String method: ...)`). The panic is nil for an absent
// method, a signature mismatch, or an ordinary call failure.
func IfaceCallStringPanic(c VMCaller, v Value, name string) (string, bool, *Panic) {
	return ifaceCallStringFatal(c, v, name)
}

// PanicPrintString returns the text Go prints for p's value after
// "panic: ", using the value's Error or String method when it has one.
// If that method itself panics, the result is "panic while printing panic
// value".
func PanicPrintString(p *Panic) string {
	if p == nil {
		return "<nil>"
	}
	pv := panicValue(p.Value, p.VC)
	if _, fatal := pv.(*panicPrintFatal); fatal {
		return "panic while printing panic value"
	}
	return fmt.Sprintf("%v", pv)
}

// IfaceCallString calls v's method name (String, Error, GoString, ...)
// when v implements it as `func() string`, the way fmt detects a
// Stringer: the method must be in v's method set (see [IfaceMember]) and
// have exactly that signature. ok is false when the method is absent or
// has another signature, when the call fails or panics, or when it
// returns a non-string.
func IfaceCallString(c VMCaller, v Value, name string) (s string, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			s, ok = "", false
		}
	}()
	m, ok := IfaceMember(c, v, name)
	if !ok || !isStringerMember(m) {
		return "", false
	}
	r, err := c.Call(m, nil)
	if err != nil {
		return "", false
	}
	s, ok = r.(string)
	return s, ok
}

// isStringerMember reports whether the member m is a real method
// callable as `func() string` — gc's requirement for error.Error,
// fmt.Stringer, and fmt.GoStringer. Field funcs and members whose
// signature cannot be verified report false: neither satisfies the
// interface, so invoking them would mis-render the value (and a
// same-named method may run unexpected side effects).
func isStringerMember(m Value) bool {
	switch fn := m.(type) {
	case *BoundMethod:
		if fn.Fn == nil || fn.Fn.Decl == nil {
			return false
		}
		return isStringerFuncType(fn.Fn)
	case *BuiltinFunc:
		// the reflected method's signature carries its receiver as
		// In(0); the bound call must take nothing and return exactly
		// `string` (a named string result does not satisfy the
		// interface, per gc's signature identity).
		if fn.Method == nil {
			return false
		}
		mt := fn.Method.Type
		return mt.NumIn() == 1 && mt.NumOut() == 1 && mt.Out(0) == reflect.TypeOf("")
	}
	return false
}

// isStringerFuncType reports whether a declared method type spells
// `func() string`: no parameters and exactly one result naming the
// predeclared string type, following package-scope alias chains —
// `func (E) Error() Text` with `type Text = string` is identical to
// `func() string` under gc's signature identity, while a defined
// `type Text string` is not.
func isStringerFuncType(fn *Function) bool {
	ft := fn.Decl.Type
	if ft == nil {
		return false
	}
	if ft.Params != nil && len(ft.Params.List) > 0 {
		return false
	}
	if ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	return isStringTypeExpr(ft.Results.List[0].Type, fn, nil)
}

// isStringTypeExpr reports whether a result-type expression resolves —
// through package-scope `type X = Y` specs only — to the predeclared
// string type. An unresolved ident is the predeclared name itself; a
// package-scope spec shadowing it must alias or the type is a defined
// type, which is never identical to string. `seen` guards alias cycles
// the parser would still have accepted (`type A = B` / `type B = A`).
func isStringTypeExpr(e ast.Expr, fn *Function, seen map[string]bool) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	if seen[id.Name] {
		return false
	}
	if spec := packageTypeSpec(fn, id.Name); spec != nil {
		if !spec.Assign.IsValid() {
			return false
		}
		if seen == nil {
			seen = map[string]bool{}
		}
		seen[id.Name] = true
		return isStringTypeExpr(spec.Type, fn, seen)
	}
	return id.Name == "string"
}

// packageTypeSpec finds the TypeSpec declaring name in the function's
// package files, or nil when the name is not declared at package scope.
func packageTypeSpec(fn *Function, name string) *ast.TypeSpec {
	if fn == nil || fn.Pkg == nil {
		return nil
	}
	for _, file := range fn.Pkg.Files {
		if file == nil || file.AST == nil {
			continue
		}
		for _, decl := range file.AST.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if ok && ts.Name.Name == name {
					return ts
				}
			}
		}
	}
	return nil
}
