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

// Unwrap peels a Named to its underlying value; any other value passes
// through unchanged. Consumption sites (index, arithmetic, marshaling)
// unwrap so a named value behaves like its underlying value.
func Unwrap(v Value) Value {
	if n, ok := v.(*Named); ok {
		return n.V
	}
	return v
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
		return &GoValue{V: td.HostNew()}
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

// Get reads the field value.
func (r *FieldRef) Get() (Value, bool) {
	s := r.structOf()
	if s == nil {
		return nil, false
	}
	for i, n := range s.Def.Fields {
		if n == r.Name {
			return s.Fields[i], true
		}
	}
	return nil, false
}

// Set writes the field value.
func (r *FieldRef) Set(v Value) bool {
	s := r.structOf()
	if s == nil {
		return false
	}
	for i, n := range s.Def.Fields {
		if n == r.Name {
			s.Fields[i] = v
			return true
		}
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

// IndexRef is the address-of a slice element (`&s[i]`) — a cell-view over
// base[key]. (Map values are unaddressable in Go, so only slices qualify.)
type IndexRef struct {
	Base Value
	Key  Value
}

// sliceOf resolves the base to the slice being referenced.
func (r *IndexRef) sliceOf() *Slice {
	v := r.Base
	for {
		if s, ok := v.(*Slice); ok {
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

// Slice resolves the base to the referenced slice — exported so the VM
// can compare two refs by backing-array identity.
func (r *IndexRef) Slice() *Slice { return r.sliceOf() }

// Get reads the element value.
func (r *IndexRef) Get() (Value, bool) {
	s := r.sliceOf()
	i, ok := r.Key.(int64)
	if s == nil || !ok || i < 0 || i >= int64(len(s.Elems)) {
		return nil, false
	}
	return s.Elems[i], true
}

// Set writes the element value.
func (r *IndexRef) Set(v Value) bool {
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

// SetRef stores through any pointer-like value: Cell, FieldRef or IndexRef.
func SetRef(v, val Value) bool {
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
			panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + typeTagOf(x.Typ)}})
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
		panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + typeTagOf(x.Typ)}})
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

// msgTypeName renders a typedef for panic text — the package NAME
// qualifier like Go's "main.T", or the anonymous type spelling.
func msgTypeName(td *TypeDef) string {
	if td == nil {
		return "?"
	}
	if td.Name != "" {
		if td.Pkg != nil && td.Pkg.Name != "" {
			return td.Pkg.Name + "." + td.Name
		}
		return td.Name
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	if x != nil {
		return anonTag(x)
	}
	return "?"
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
			panic(&Panic{Value: &RuntimeError{Msg: "hash of unhashable type " + typeTagOf(x.Typ)}})
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
	case *TypedNil, *IfaceNil, Nil:
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
		if td.Pkg != nil && td.Pkg.Path != "" {
			return td.Pkg.Path + "." + td.Name
		}
		return td.Name
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
	// Typ is the declared slice type when one is known (a named literal or
	// a `var s S` bind): element stores re-coerce and named slice types
	// keep their identity on rebinds.
	Typ *TypeDef
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

	// Interfaces: MReqs are the directly declared method names; IEmbeds are
	// the embedded element expressions (io.Reader, ~int unions, ...). The
	// full required set is computed by the engine on demand.
	MReqs   []string
	IEmbeds []ast.Expr

	// Embedding: for each embedded struct field (no declared name),
	// EmbedSpecs[i] is its type AST and EmbedIdx[i] its index in Fields.
	// Resolved lazily into Embeds by the engine's FindMethod/MethodsOf.
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
	// NilArr marks an 'i' iterator walking the indices of a nil *[N]T —
	// the index sequence is legal Go but reading an element derefs nil.
	NilArr bool
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
	if len(frames) <= maxTracebackEntries {
		return strings.Join(frames, "\n")
	}
	half := maxTracebackEntries / 2
	return strings.Join(frames[:half], "\n") +
		fmt.Sprintf("\n... %d frames elided ...\n", len(frames)-maxTracebackEntries) +
		strings.Join(frames[len(frames)-half:], "\n")
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
