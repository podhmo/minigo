package runtime

import (
	"go/ast"
	"reflect"
)

// Capability interfaces (P2-1): the optional powers a caller may have
// beyond VMCaller. *vm.VM implements all of them; thinner callers —
// process-scoped wrappers, test fakes — implement a subset, so a site
// reaches a capability through a type assertion and treats ok=false as
// "this caller cannot do it", never a bug. VMCaller stays the union:
// it declares only what every caller must have, and these stay outside
// it so a thin caller is not forced to stub them.

// HostMapDeleter runs delete on a boxed host map (url.Values,
// http.Header).
type HostMapDeleter interface {
	HostMapDelete(m *GoValue, key Value) (ok bool)
}

// HostClearer runs clear on a boxed host map or slice.
type HostClearer interface {
	HostClear(m *GoValue) (ok bool)
}

// HostNilTwiner reports whether two typedefs are the host *T and its
// source (or bound) twin — the pair reflect.DeepEqual must not
// distinguish.
type HostNilTwiner interface {
	HostNilTwin(a, b *TypeDef) bool
}

// HostNamedTwinner reports whether td is the typedef the host type rt
// resolves to — the interned-descriptor answer an engine with a type
// intern table gives where [HostNamedIs] only compares name and path.
type HostNamedTwinner interface {
	HostNamedTwin(rt reflect.Type, td *TypeDef) bool
}

// ConstMaterializer converts an untyped constant to a value of type td,
// reporting overflow instead of trapping.
type ConstMaterializer interface {
	MaterializeConstErr(u *UConst, td *TypeDef) (Value, error)
}

// CatchCaller calls a script value like [VMCaller.Call] but recovers a
// script panic into an error — used when a host callback must not let
// a panic cross reflect.MakeFunc into the host.
type CatchCaller interface {
	CallCatching(callee Value, args []Value) (Value, error)
}

// Narrow caller capabilities (P2-2): a helper takes the smallest
// interface covering the methods it calls, so a thinner caller can
// serve it without implementing all of VMCaller. Every VMCaller
// satisfies each of these.

// ScriptCaller just calls a script value.
type ScriptCaller interface {
	Call(fn Value, args []Value) (Value, error)
}

// MemberLookup resolves base.name and answers method-set probes.
type MemberLookup interface {
	Member(base Value, name string) (m Value, ok bool)
	MethodSetOf(v Value) (set map[string]bool, unsure bool)
}

// MemberCaller probes a member then calls it — the common "find the
// method, invoke it" helper shape.
type MemberCaller interface {
	ScriptCaller
	MemberLookup
}

// MemberZeroCaller adds typed-zero construction to MemberCaller.
type MemberZeroCaller interface {
	MemberCaller
	Zeroer
}

// Zeroer makes the Go zero value of a typedef.
type Zeroer interface {
	Zero(td *TypeDef) Value
}

// ElemTyper answers a container typedef's element type.
type ElemTyper interface {
	ElemOf(td *TypeDef) (*TypeDef, error)
}

// ExprResolver resolves a type expression in a typedef's declaring
// context.
type ExprResolver interface {
	ResolveType(from *TypeDef, x ast.Expr) (*TypeDef, error)
}

// SeqTyper answers the key/value types a range-over-func argument
// needs — map key type via ResolveType, element type via ElemOf.
type SeqTyper interface {
	ExprResolver
	ElemTyper
}

// FieldTyper answers a struct typedef's field types.
type FieldTyper interface {
	FieldTypes(td *TypeDef) ([]*TypeDef, error)
}

// ShapeTyper is the type-query set shape-driven helpers need: type
// expression resolution, element types, and struct field lists.
type ShapeTyper interface {
	ExprResolver
	ElemTyper
	FieldTyper
}

// MemberShapeCaller probes members, calls them, and answers shape
// queries — the encoding/json helpers' full capability.
type MemberShapeCaller interface {
	MemberCaller
	ShapeTyper
}

// ZeroElemTyper makes typed zeros and answers element types — what a
// helper building a fresh embedded struct field needs.
type ZeroElemTyper interface {
	Zeroer
	ElemTyper
}

// MemberShapeZeroCaller is MemberShapeCaller plus typed-zero
// construction — the encoding/json unmarshal path's full capability.
type MemberShapeZeroCaller interface {
	MemberShapeCaller
	Zeroer
}

// CallerRef is an opaque caller handed to helpers that only probe it
// for optional capabilities (constAtWidth's ConstMaterializer) or
// forward it opaquely — nothing is required of it statically.
type CallerRef = any

// ValTyper answers the typedef of a runtime value.
type ValTyper interface {
	TypeOf(x Value) *TypeDef
}

// Converter applies the T(x) conversion.
type Converter interface {
	Convert(td *TypeDef, x Value) (Value, error)
}

// Equaler compares two values of a typedef.
type Equaler interface {
	Equal(td *TypeDef, a, b Value) bool
}

// ElemZeroer builds the element-type zero of a container typedef.
type ElemZeroer interface {
	ElemZero(td *TypeDef) Value
}

// ArraySizer reports an array typedef's length.
type ArraySizer interface {
	ArrayLenOf(td *TypeDef) (n int64, ok bool)
}

// MapKeyer applies the declared-key-type check indexing runs.
type MapKeyer interface {
	MapKeyOperand(mapTd *TypeDef, key Value) Value
}

// NewMapper builds an empty map for a map typedef.
type NewMapper interface {
	NewMap(td *TypeDef) *Map
}

// ConvertTyper converts values and types them — the unify/min-max
// operand helpers' capability.
type ConvertTyper interface {
	Converter
	ValTyper
}

// ElemZeroTyper types values and builds element zeros — the
// errors-matcher and json-alloc helpers' capability.
type ElemZeroTyper interface {
	ValTyper
	ElemZeroer
}

// TypeMemberCaller calls members and types values — the encoding/json
// error path's capability.
type TypeMemberCaller interface {
	MemberCaller
	ValTyper
}

// MemberShapeTyper is MemberShapeCaller plus value typing — the
// encoding/json marshal entry's capability.
type MemberShapeTyper interface {
	MemberShapeCaller
	ValTyper
}

// ErrMatchCaller probes members and types/zeros values — the
// errors.As/Is target matcher's capability.
type ErrMatchCaller interface {
	MemberLookup
	ElemZeroTyper
}

// ElemOperandTyper converts values and answers element typedefs — the
// element-operand helpers' capability.
type ElemOperandTyper interface {
	Converter
	ElemTyper
}

// IndexTyper converts, compares and answers element typedefs — the
// slice-index helpers' capability.
type IndexTyper interface {
	Converter
	Equaler
	ElemTyper
}
