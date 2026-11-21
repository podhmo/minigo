// spell.go re-exports the reflect spellings vm code uses, so vm keeps
// its logic while the reflect import itself stays inside this package:
// a `reflect.Value` annotation becomes `Value` here, `reflect.TypeOf`
// becomes `TypeOf`, kind compares become `KindX` constants.
package hostbridge

import (
	"reflect"
	"unsafe"
)

// Value is [reflect.Value]; Type is [reflect.Type]; Kind is
// [reflect.Kind]; ChanDir is [reflect.ChanDir]; Method is
// [reflect.Method].
type (
	Value     = reflect.Value
	Type      = reflect.Type
	Kind      = reflect.Kind
	ChanDir   = reflect.ChanDir
	StructTag = reflect.StructTag
	Method    = reflect.Method
)

// Kind constants used by vm's host-value dispatch.
const (
	KindInvalid       = reflect.Invalid
	KindBool          = reflect.Bool
	KindInt           = reflect.Int
	KindInt8          = reflect.Int8
	KindInt16         = reflect.Int16
	KindInt32         = reflect.Int32
	KindInt64         = reflect.Int64
	KindUint          = reflect.Uint
	KindUint8         = reflect.Uint8
	KindUint16        = reflect.Uint16
	KindUint32        = reflect.Uint32
	KindUint64        = reflect.Uint64
	KindUintptr       = reflect.Uintptr
	KindFloat32       = reflect.Float32
	KindFloat64       = reflect.Float64
	KindComplex64     = reflect.Complex64
	KindComplex128    = reflect.Complex128
	KindArray         = reflect.Array
	KindChan          = reflect.Chan
	KindFunc          = reflect.Func
	KindInterface     = reflect.Interface
	KindMap           = reflect.Map
	KindPointer       = reflect.Pointer
	KindPtr           = reflect.Ptr
	KindSlice         = reflect.Slice
	KindString        = reflect.String
	KindStruct        = reflect.Struct
	KindUnsafePointer = reflect.UnsafePointer
	KindRecvDir       = reflect.RecvDir
	KindSendDir       = reflect.SendDir
	KindBothDir       = reflect.BothDir
)

// TypeOf / RVOf / ZeroOf / NewRV / PointerTo / DeepEqual are the reflect
// functions vm code calls, spelled without the import.
func TypeOf(v any) Type       { return reflect.TypeOf(v) }
func TypeForOf[T any]() Type  { return reflect.TypeFor[T]() }
func ZeroOf(t Type) Value     { return reflect.Zero(t) }
func NewRV(t Type) Value      { return reflect.New(t) }
func PointerTo(t Type) Type   { return reflect.PointerTo(t) }
func DeepEqual(a, b any) bool { return reflect.DeepEqual(a, b) }
func KindOf(rv Value) Kind    { return rv.Kind() }
func IsFunc(rv Value) bool    { return rv.IsValid() && rv.Kind() == reflect.Func }
func IsPointer(rv Value) bool {
	return rv.IsValid() && (rv.Kind() == reflect.Ptr || rv.Kind() == reflect.UnsafePointer)
}
func IsInterface(rv Value) bool { return rv.IsValid() && rv.Kind() == reflect.Interface }
func IsStruct(rv Value) bool    { return rv.IsValid() && rv.Kind() == reflect.Struct }
func IsStringV(rv Value) bool   { return rv.IsValid() && rv.Kind() == reflect.String }
func TypeString(v any) string   { return reflect.TypeOf(v).String() }
func PointerToElem(t Type) bool { return t != nil && t.Kind() == reflect.Pointer }

// NewAt is reflect.NewAt — unexported-field writes through UnsafeAddr.
func NewAt(t Type, p unsafe.Pointer) Value { return reflect.NewAt(t, p) }

// Type constructors used by typedef synthesis.
func SliceOf(t Type) Type                                  { return reflect.SliceOf(t) }
func MapOf(k, v Type) Type                                 { return reflect.MapOf(k, v) }
func ChanTypOf(d ChanDir, t Type) Type                     { return reflect.ChanOf(d, t) }
func FuncOf(in, out []Type, variadic bool) Type            { return reflect.FuncOf(in, out, variadic) }
func PtrTo(t Type) Type                                    { return reflect.PointerTo(t) }
func MakeFunc(t Type, fn func(args []Value) []Value) Value { return reflect.MakeFunc(t, fn) }
