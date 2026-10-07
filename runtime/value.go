// Package runtime holds the minigo value model, environments, and the lazy
// package machinery. Values are boxed `any` for now; a tagged
// representation is a later optimization.
package runtime

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/syntax"
)

// Value is a boxed interpreter value. Concrete types:
//
//	int64, float64, string, bool, Nil,
//	*Cell, *Slice, *Map, *Struct, *TypeDef,
//	*Function, *Closure, *BoundMethod, *BuiltinFunc, *Tuple,
//	*Package, *Iterator, *GoValue, *TypedNil, *IfaceNil, *Named
type Value = any

// Nil is the nil value.
type Nil struct{}

// NIL is the singleton nil.
var NIL Value = Nil{}

// TypedNil is a nil value that knows its (non-interface) type: the zero of
// `var p *int`, `var s []int`, `(*int)(nil)`, `return nil` under a *T
// result. It still compares equal to nil — a nil pointer IS nil in Go —
// but carries enough type information to distinguish `x == nil` once it
// is stored into an interface-typed slot (see IfaceNil), to report the
// right zero through comma-ok asserts, and to panic on deref like a real
// nil pointer.
type TypedNil struct{ Typ *TypeDef }

// IfaceNil is a TypedNil that crossed an interface-typed boundary
// (`var x any = (*int)(nil)`, an `any` parameter, an `any` result). The
// interface records a dynamic type, so unlike a bare TypedNil it is NOT
// nil: `x == nil` is false, `if x` is true, and `x.(T)` sees the
// recorded dynamic type.
type IfaceNil struct{ Typ *TypeDef }

// IfaceTaggedNil returns the interface typedef carried by a nil
// interface value — an IfaceNil whose static tag is an interface kind
// — else nil. An untagged IfaceNil (Typ == nil) has no tag to report,
// and a concrete tag means a boxed typed nil, not the nil interface.
func IfaceTaggedNil(v Value) *TypeDef {
	if in, ok := v.(*IfaceNil); ok && in.Typ != nil && in.Typ.Kind == KindInterface {
		return in.Typ
	}
	return nil
}

// BoxedNilTyp returns the dynamic typedef of a typed nil boxed in an
// interface — an IfaceNil whose tag is a concrete type — else nil.
// The distinction is observable: the boxed nil is NOT nil (it has a
// dynamic type), `x == nil` fails and `x.(T)` sees the tag.
func BoxedNilTyp(v Value) *TypeDef {
	if in, ok := v.(*IfaceNil); ok && in.Typ != nil && in.Typ.Kind != KindInterface {
		return in.Typ
	}
	return nil
}

// IsNilIface reports whether v is the nil interface itself — an
// IfaceNil with no dynamic type: untagged (Typ == nil) or carrying
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

// Named is a value of a declared named basic type (`type MyInt int`,
// `type A B`): the underlying value plus the typedef it was declared
// under. Zeros, coercion sites and conversions tag the value so its
// declared identity survives reads — method dispatch, type asserts and
// generic inference see MyInt, not the bare int64 it stores. Everywhere
// else treats it as V (use Unwrap).
type Named struct {
	Typ *TypeDef // the declared typedef (a defined type, never a builtin)
	V   Value    // the underlying value
}

// Tag attaches the declared-type tag td to v. An existing Named tag is
// peeled first — values are never tagged twice, so `Tag(T, Named{U})`
// yields `Named{T, inner}` rather than the `Named{Named{...}}` shape
// that needed peeling downstream. This is the one place Named values
// get constructed.
func Tag(td *TypeDef, v Value) *Named {
	return &Named{Typ: td, V: Unwrap(v)}
}

// TagOf reads the outermost declared-type tag of v, or nil when v is
// untagged. Use it where the tag itself matters — method dispatch, type
// assertions, formatting — and Unwrap where the underlying value does.
func TagOf(v Value) *TypeDef {
	if n, ok := v.(*Named); ok {
		return n.Typ
	}
	return nil
}

// Unwrap peels every Named tag from v; any other value passes through
// unchanged. Consumption sites (index, arithmetic, marshaling) unwrap
// so a named value behaves like its underlying value.
func Unwrap(v Value) Value {
	for {
		if n, ok := v.(*Named); ok {
			v = n.V
			continue
		}
		return v
	}
}

// Zero returns the zero value of a typedef: a Struct with nil fields,
// a TypedNil for nilable kinds (pointer, slice, map, chan, func), NIL for
// interface types, and the matching literal zero for basic types. Named
// basics whose underlying type fails to resolve yield a TypedNil hole
// rather than a fake scalar.
func Zero(td *TypeDef) Value {
	if td == nil {
		return NIL
	}
	if td.HostNew != nil {
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

// Cell is a mutable slot. Every declared variable is a cell, which makes
// closures, pointers and addressable receivers uniform: a pointer IS a cell.
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

// FieldRef is the address-of a struct field (`&s.f`) — a cell-view over
// base.name. The base resolves at access time (struct value or pointer).
type FieldRef struct {
	Base Value
	Name string
}

// structOf resolves the base to the struct being referenced.
func (r *FieldRef) structOf() *Struct {
	v := r.Base
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
				if emb := (&FieldRef{Base: st.Fields[i]}).structOf(); emb != nil {
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

// Find resolves the field slot the ref points at — the owning struct
// and field index after embedded promotion, so equality and address
// identity can compare storage rather than selector spellings.
func (r *FieldRef) Find() (*Struct, int, bool) { return r.find() }

// Get reads the field value.
func (r *FieldRef) Get() (Value, bool) {
	if st, idx, ok := r.find(); ok {
		return st.Fields[idx], true
	}
	return nil, false
}

// Set writes the field value.
func (r *FieldRef) Set(v Value) bool {
	if st, idx, ok := r.find(); ok {
		st.Fields[idx] = v
		return true
	}
	return false
}

// DerefRef is the location `*p` names when the pointer itself has a
// tracked storage ref: `*p = v` resolves what the pointer refers to at
// store time, so a RHS that reseats p still writes the live pointee.
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

// SharedElem reports whether an interior write through v reaches shared
// storage — Go allows `m[k][i] = v` and `m[k].f = v` on a map element
// only when the element is a reference (slice, map, chan, func) or a
// pointer, because the element read is a copy. Arrays, structs, scalars
// and strings are copies: their interiors cannot be assigned through
// `m[k]` at all.
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

// IndexRef is the address-of a slice element (`&s[i]`) — a cell-view over
// base[key]. (Map values are unaddressable in Go, so a ref over a map
// only resolves reference-shaped elements; see SharedElem.)
type IndexRef struct {
	Base Value
	Key  Value
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

// Slice resolves the base to the referenced slice — exported so the VM
// can compare two refs by backing-array identity.
func (r *IndexRef) Slice() *Slice { return r.sliceOf() }

// Map resolves the base to the referenced map, or nil — exported so the
// VM can tell a map-element lvalue (m[k], a copy in Go) apart from a
// slice-element one.
func (r *IndexRef) Map() *Map { return r.mapOf() }

// Get reads the element value.
func (r *IndexRef) Get() (Value, bool) {
	if m := r.mapOf(); m != nil {
		// a missing key reports no value — callers needing the zero
		// go through the VM's index path, which knows the elem typedef.
		v, ok := m.Get(r.Key)
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
	i, ok := r.Key.(int64)
	if s == nil || !ok || i < 0 || i >= int64(len(s.Elems)) {
		return nil, false
	}
	return s.Elems[i], true
}

// Set writes the element value.
func (r *IndexRef) Set(v Value) bool {
	if m := r.mapOf(); m != nil {
		m.Insert(r.Key, v)
		return true
	}
	s := r.sliceOf()
	i, ok := r.Key.(int64)
	if s == nil || !ok || i < 0 || i >= int64(len(s.Elems)) {
		return false
	}
	s.Elems[i] = v
	return true
}

// Deref unwraps any pointer-like value one level: Cell, FieldRef or
// IndexRef. It reports false for non-references. A Named value is
// transparent: its underlying value decides (a named pointer type
// dereferences through its cell, a named scalar does not).
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

// Copy implements Go assignment semantics: structs copy by value —
// recursively, since a struct field is itself a copied value — while
// slices, maps, pointers, channels and funcs share. Storing without it
// leaves the target aliasing the source's fields (`var k = p.key;
// p.key.mark.c++` would leak into k).
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
		// assignment copies the underlying value but keeps the declared tag
		return Tag(x.Typ, Copy(x.V))
	}
	return v
}

// SetRef stores through any pointer-like value: Cell, FieldRef or IndexRef.
// The stored value is copied like any Go assignment — otherwise a struct
// RHS would alias the slot.
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

// CanonicalKey renders a map key to a Go-comparable value so composite
// keys compare by content. Scalars pass through — a float64 NaN keeps
// Go's never-equal map semantics for free. Pointers and channels keep
// identity (they ARE their identity); structs and fixed-size arrays
// fold to a mapKey of the type name plus a recursive field rendering
// where any NaN inside forces a never-equal nonce, like Go.
// Unhashable keys (slices, maps, funcs) panic like Go's runtime does.
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
		writeKeyRepr(&sb, x.Fields)
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

// UConstNative materializes an untyped constant to its default-type
// value — the conversion every host crossing and VM slot shares:
// bool/string/int64/float64/complex128 with Go's int/float overflow
// errors and the constant -0 fold. The VM-side dressings stay with
// the caller: the rune tag for a Rune constant and the GoValue wrap a
// complex result needs inside the VM.
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

// CanonConstZero folds a materialized constant -0 to +0: untyped
// constants have no negative zero — literal -0.0 and underflowing
// magnitudes like -1e-10000 both read +0 in Go
// ($GOROOT/test/fixedbugs/issue12577.go). Apply it wherever a constant
// becomes a float64; runtime-computed -0 (from -x on a nonzero var)
// never passes through here.
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
		writeKeyRepr(sb, x.Fields)
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

func nextKeyNonce() int64 {
	return keyNonce.Add(1)
}

// PtrArrayType returns the fixed-size ArrayType behind a *[N]T
// typedef, or nil — `var p *[3]int` peels the StarExpr to its array.
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

// Tuple packs multiple values (multi return / multi assign).
type Tuple struct{ Elems []Value }

// Slice is a Go slice value.
type Slice struct {
	Elems []Value
	// N is the logical length of a slice whose zero-size elements were
	// never materialized — make([]struct{}, n) allocates 0 bytes for any
	// n, like Go's zerobase ($GOROOT/test/fixedbugs/issue29190.go).
	// Index reads vend Zero and element writes are unobservable (a
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

// Virtual reports whether the slice's backing is implicit — a
// zero-size-element slice that never materialized its elements. N and
// CapN carry the logical length and capacity while Elems stays empty;
// a virtual slice degraded to len/cap 0 (s[:0:0]) reports false, which
// is honest — it IS an ordinary empty slice at that point.
func (s *Slice) Virtual() bool { return s.N > 0 || s.CapN > 0 }

// Len reports the slice's logical length — N for a virtual slice whose
// elements were never materialized, len(Elems) otherwise.
func (s *Slice) Len() int64 {
	if s.Virtual() {
		return s.N
	}
	return int64(len(s.Elems))
}

// Cap reports the slice's logical capacity — CapN for a virtual slice,
// cap(Elems) otherwise.
func (s *Slice) Cap() int64 {
	if s.Virtual() {
		return s.CapN
	}
	return int64(cap(s.Elems))
}

// Spread wraps the operand of a trailing `xs...` call argument when the
// slice is virtual: it cannot expand into one argument per element
// (the count is astronomical), so it rides to the callee as a single
// lazy value. append and variadic-parameter binding expand the logical
// length; every other callee traps on the marker rather than answering
// wrong.
type Spread struct{ S *Slice }

// IsZeroSizeValue reports whether a value's type occupies no bytes — an
// empty struct, a [0]T array, or a composite of only zero-size fields.
// Go stores all such values at the same address (runtime.zerobase), and
// make([]T, n) on a zero-size element allocates 0 bytes for any n.
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

// Map is a Go map value (keys must be comparable basics for now).
type Map struct {
	Pairs map[Value]Value
	Order []Value // insertion order for stable-ish range
	Keys  []Value // canonical key per Order slot — a NaN canonical key is
	// a fresh nonce on every CanonicalKey call, so iteration remembers
	// the one used at insert instead of recomputing it.
	Typ *TypeDef // declared map type (nil => missing keys yield NIL)
}

// Len reports the number of stored pairs.
func (m *Map) Len() int {
	return len(m.Order)
}

// At returns the i-th pair in insertion order — the key as written at
// insert plus its current value.
func (m *Map) At(i int) (Value, Value) {
	return m.Order[i], m.Pairs[m.Keys[i]]
}

// Get reports the value stored under k's canonical key. Raw-key lookup
// only: callers already holding a canonical key (iterating Pairs) read
// the table directly — re-canonicalizing a canonical key (a NaN nonce)
// never reconstructs it.
func (m *Map) Get(k Value) (Value, bool) {
	v, ok := m.Pairs[CanonicalKey(k)]
	return v, ok
}

// SnapshotKeys copies the map's iteration state — the display keys as
// written at insert plus the canonical key each hashes under — for a
// new iterator. The canonical copy matters: a NaN key's canonical form
// is a fresh nonce on every CanonicalKey call, so probing Pairs by the
// stored key (LookupCanonical) reports live presence where Get on the
// display key would report every NaN entry as deleted.
func (m *Map) SnapshotKeys() (display, canonical []Value) {
	return append([]Value(nil), m.Order...), append([]Value(nil), m.Keys...)
}

// LookupCanonical reads the pair stored under a canonical key — one
// from SnapshotKeys or iterating Pairs directly. Raw keys must go
// through Get: re-canonicalizing a canonical key never reconstructs it.
func (m *Map) LookupCanonical(ck Value) (Value, bool) {
	v, ok := m.Pairs[ck]
	return v, ok
}

// Insert stores v under k's canonical key, appending k to the insertion
// order only when the key is new. Keeping canonicalization and the
// order/keys append in one place is what makes the NaN-nonce key policy
// hold at every write site.
func (m *Map) Insert(k, v Value) {
	ck := CanonicalKey(k)
	if m.Pairs == nil {
		m.Pairs = map[Value]Value{}
	}
	if _, exists := m.Pairs[ck]; !exists {
		m.Order = append(m.Order, k)
		m.Keys = append(m.Keys, ck)
	}
	m.Pairs[ck] = v
}

// Delete drops k's canonical key and its insertion-order slot — a stale
// order entry would render and range as `k:<nil>`. The canonical key
// stored per slot tells which one to drop; a NaN slot's stored nonce can
// never be recomputed, matching Go's unreachable-NaN-key semantics.
// Reports whether the key was present.
func (m *Map) Delete(k Value) bool {
	ck := CanonicalKey(k)
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

// Clear drops every pair and order slot.
func (m *Map) Clear() {
	m.Pairs = map[Value]Value{}
	m.Order = nil
	m.Keys = nil
}

// Chan is a channel value backed by a real host channel: sends and
// receives block exactly as in Go, capacity is honored, and close wakes
// every parked receiver. Blocking operations also watch the owning
// process's done channel so a dead process releases parked goroutines.
type Chan struct {
	C chan Value
	// Typ is the declared channel type when one is known (make or a
	// `var c C` bind): sends re-coerce to the element type.
	Typ *TypeDef
}

// SelArm is one prepared select case, built by OpSelArm on entry to a
// select: the reflect.SelectCase to poll plus the receive-bind shape
// (NRecv) or send marker, and the channel's element typedef for
// closed-receive zero values.
type SelArm struct {
	Case  reflect.SelectCase
	Send  bool
	NRecv int
	ETyp  *TypeDef
}

// Task is the handle of one spawned goroutine: Done closes when its call
// ends — normally, by panic (Err), or by proc exit — and Wait reports
// how it finished. Parent chains mirror the spawn tree so callers can
// recognize when waiting would deadlock (dependency cycles).
type Task struct {
	Done    chan struct{}
	Err     error
	Parent  *Task
	Aborted bool // finished by process exit rather than its own outcome
	// Result carries the call's return value to a joiner — set before
	// Done closes, so a Wait that returns also observes it.
	Result Value
}

// Finish records the task's outcome and releases waiters. Called once.
func (t *Task) Finish(err error, aborted bool) {
	t.Err = err
	t.Aborted = aborted
	close(t.Done)
}

// Wait blocks until the task ends and reports its call's error (the
// process-exit sentinel for aborted tasks).
func (t *Task) Wait() error {
	<-t.Done
	return t.Err
}

// TypeDef is a runtime type descriptor for a named type.
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
	// method set. Set only on bound intrinsics' typedefs.
	HostNew func() any

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
}

// CachedFieldTypes returns the field typedefs stored by SetFieldTypes.
func (td *TypeDef) CachedFieldTypes() ([]*TypeDef, bool) {
	fts, ok := td.fieldTypes.Load().([]*TypeDef)
	return fts, ok
}

// SetFieldTypes caches td's resolved field typedefs. They depend only on
// td's own declaration context, so every reader of td can share them.
func (td *TypeDef) SetFieldTypes(fts []*TypeDef) { td.fieldTypes.Store(fts) }

// CachedIfaceReqs returns the requirement names stored by SetIfaceReqs.
func (td *TypeDef) CachedIfaceReqs() (map[string]bool, bool) {
	reqs, ok := td.ifaceReqs.Load().(map[string]bool)
	return reqs, ok
}

// SetIfaceReqs caches an interface typedef's flattened requirement
// names. Like the field typedefs they depend only on td's declaration
// context; callers must treat the shared map as read-only.
func (td *TypeDef) SetIfaceReqs(reqs map[string]bool) { td.ifaceReqs.Store(reqs) }

// CachedIfaceSigs returns the signature shells stored by SetIfaceSigs.
func (td *TypeDef) CachedIfaceSigs() (map[string]*Function, bool) {
	sigs, ok := td.ifaceSigs.Load().(map[string]*Function)
	return sigs, ok
}

// SetIfaceSigs caches an interface typedef's signature shells (nil
// when it declares none). Stable shell pointers also let SigMemo key
// signature comparisons; callers must treat the map as read-only.
func (td *TypeDef) SetIfaceSigs(sigs map[string]*Function) { td.ifaceSigs.Store(sigs) }

// ResetCaches drops the lazily computed caches — for a shallow copy
// whose resolution context differs from the original's.
func (td *TypeDef) ResetCaches() {
	td.fieldTypes = atomic.Value{}
	td.ifaceReqs = atomic.Value{}
	td.ifaceSigs = atomic.Value{}
}

// TypeKind classifies a named type's underlying shape.
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

// Struct is an instance of a KindStruct TypeDef.
type Struct struct {
	Def    *TypeDef
	Fields []Value
}

// BoundMethod binds a receiver to a function.
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

// BuiltinFunc is a host-native function (len, println, host intrinsics).
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
}

// VMCaller is the piece of the VM builtins need (kept narrow to avoid a
// runtime->vm dependency).
type VMCaller interface {
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
	Copy(x Value) Value
	// Spawn runs fn(args) on a new goroutine sharing the caller's
	// process — the machinery of the `go` statement, exposed so host
	// code (task runners) can fan out work the same way. A non-procExit
	// failure fails the whole process.
	Spawn(fn Value, args []Value) *Task
	// Task returns the handle of the calling goroutine — nil on the
	// root goroutine — for ancestry-aware cycle checks.
	Task() *Task
	// ArrayLenOf reports the element count of an array typedef — an
	// *ast.ArrayType that kept its length ([3]int, [N]int, [N*2]int);
	// ok=false for non-array shapes. Lets len()/cap() on a nil *[N]T
	// constant-fold like Go without a live frame.
	ArrayLenOf(td *TypeDef) (n int64, ok bool)
	// CallerPCs returns opaque uintptr handles for the call stack —
	// live frames plus frames already unwound by the in-flight panic,
	// top-first like runtime.Callers.
	CallerPCs() []uintptr
	// CallerFrame resolves a handle from CallerPCs to its call site.
	CallerFrame(pc uintptr) (site CallSite, ok bool)
}

// CallSite is one call-stack entry for runtime.Callers: the function's
// Go-style symbol name and the source position it is executing.
type CallSite struct {
	Name string
	File string
	Line int
}

// Function is a compiled-or-compilable function. Chunk is produced lazily
// on first call via Compile.
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
	// parameters a function literal closes over — set at OpMakeClosure
	// when the enclosing function's binds merge into the proto, so a
	// local type declared in the body still differs per instantiation.
	OuterTParams []string

	Compile func(*Function) error // injected by the engine
	once    sync.Once
	cerr    error
	Chunk   *bytecode.Chunk
}

// EnsureCompiled compiles the function on first use.
func (f *Function) EnsureCompiled() error {
	f.once.Do(func() {
		if f.Compile != nil {
			f.cerr = f.Compile(f)
		}
	})
	return f.cerr
}

// WithBinds returns a copy of f carrying binds. The copy shares f's
// compiled chunk (compiling f first if needed) and never recompiles —
// a field-wise copy instead of `*f` keeps the sync.Once uncopied.
func (f *Function) WithBinds(binds map[string]Value) *Function {
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

// OuterParamNames lists the type-parameter names visible from this
// function's frame in declaration order — the enclosing instantiation's
// params first (closed over by literals), then the function's own.
func (f *Function) OuterParamNames() []string {
	if len(f.OuterTParams) == 0 {
		return f.TParams
	}
	names := make([]string, 0, len(f.OuterTParams)+len(f.TParams))
	names = append(names, f.OuterTParams...)
	names = append(names, f.TParams...)
	return names
}

// Closure is a function value with captured upvalue cells.
type Closure struct {
	Fn     *Function
	Upvals []*Cell
}

// Iterator is the state of an in-progress range loop.
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
	ChRV reflect.Value
	ETyp *TypeDef

	// Fn is the producer for 'f' (range-over-func) iterators. Started marks
	// that the producer was invoked once; Exited marks that the loop body
	// abandoned the loop (break/goto/return) or died on panic, after which
	// further yield calls must panic.
	Fn      Value
	Started bool
	Exited  bool
}

// GoValue wraps a host reflect value at the FFI boundary (implemented in
// ffi.go; declared here as the box used by Globals/Register).
type GoValue struct{ V any }

// UConst is a lazily materialized untyped constant: a rune literal
// (default rune, not int), an integer that does not fit int64, a float
// literal overflowing float64, or a complex constant. Const cells hold
// it lazily; conversion, assignment, arithmetic and call boundaries
// materialize it to the constant's default type — matching Go, where
// `const B = 1<<100` compiles while `var x = 1<<100` is rejected.
type UConst struct {
	V    constant.Value
	Rune bool // Kind()==Int but the literal/expression is rune-flavored
}

// DefaultName spells the Go type an untyped constant defaults to.
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

// SymbolID is a canonical symbol address used for special-form dispatch:
// the defining package's import path plus the member name.
type SymbolID struct {
	PackagePath string
	Name        string
}

// QuotedCall is a special-form call site: the call and its arguments are
// kept as AST together with the caller's scope (name -> slot maps), so a
// handler can evaluate or inspect them on demand — partial evaluation.
type QuotedCall struct {
	Call   *ast.CallExpr
	File   *syntax.File
	Locals map[string]int // visible local name -> caller slot index
	Upvals map[string]int // visible upvalue name -> caller upvalue index
}

// SpecialFunc is a special-form handler. It fires only when the VM reaches
// the SPECIAL_CALL site; arguments arrive quoted, not evaluated.
type SpecialFunc func(ctx SpecialContext, call *QuotedCall) (Value, error)

// SpecialContext is the narrow interpreter surface handed to special-form
// handlers: position/scope access plus on-demand evaluation of quoted
// expressions inside the caller's frame.
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

// Panic is a script-level panic value; catchable by recover().
type Panic struct {
	Value   Value
	Frames  []string // "func at file:line" entries collected while unwinding
	GoStack string   // host goroutine stack at panic time (host panics only)
}

func (p *Panic) Error() string {
	s := fmt.Sprintf("panic: %v", panicValue(p.Value))
	if len(p.Frames) > 0 {
		s += "\nTraceback (most recent call first):\n" + renderFrames(p.Frames)
	}
	if p.GoStack != "" {
		s += "\n" + p.GoStack
	}
	return s
}

// RuntimeError is the payload of a runtime panic (bounds, nil deref,
// makeslice, ...): recover() yields it as an error value the way Go's
// *runtime.Error does — `err.(error)` and `.Error()` work on it.
type RuntimeError struct{ Msg string }

func (e *RuntimeError) Error() string { return "runtime error: " + e.Msg }

// PanicNilError is the payload recover() sees for panic(nil), matching
// the *runtime.PanicNilError Go produces since 1.21.
type PanicNilError struct{}

func (*PanicNilError) Error() string {
	return "runtime error: panic called with nil argument"
}

// panicValue renders the panic payload for messages: a boxed host value
// (error, stringer) is unwrapped so `panic(err)` reads like Go's output
// rather than a struct dump.
func panicValue(v Value) any {
	if gv, ok := v.(*GoValue); ok {
		return gv.V
	}
	return v
}

// Trap is a VM-level failure (unsupported construct, invalid operation).
// It bypasses script recover() and unwinds to the engine boundary.
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

// StructFieldTags reads the raw tag literals of a struct type (`name
// string `json:"name"“) into a field-name → tag map. Callers store it
// on TypeDef.FTags so tag-aware hosts (encoding/json) can look keys up.
func StructFieldTags(st *ast.StructType) map[string]string {
	var out map[string]string
	for _, f := range st.Fields.List {
		if f.Tag == nil || len(f.Names) == 0 {
			continue
		}
		tag, err := strconv.Unquote(f.Tag.Value)
		if err != nil {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		for _, n := range f.Names {
			out[n.Name] = tag
		}
	}
	return out
}

// IfaceMember selects a member through the interface lens Go's implicit
// assertions apply: pointer-receiver methods are absent from a value's
// method set, so satisfaction probes (fmt's Stringer check, host-iface
// adapters like Locker, sort.Interface, io.Reader/Writer, error Unwrap)
// skip a method a bare value cannot offer. When the engine offers no
// method set — or reports it unsure — selection falls back to Member's
// existence check.
func IfaceMember(c VMCaller, v Value, name string) (Value, bool) {
	if set, unsure := c.MethodSetOf(v); set != nil && !set[name] && !unsure {
		return nil, false
	}
	return c.Member(v, name)
}
