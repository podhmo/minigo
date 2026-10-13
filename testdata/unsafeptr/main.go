package unsafeptr

import (
	"fmt"
	"unsafe"
)

// ---- construction and identity ----

func PtrNonNil() int {
	var n int
	p := unsafe.Pointer(&n)
	if p == nil {
		return 0
	}
	return 1
}

func ZeroIsNil() int {
	var p unsafe.Pointer
	if p == nil {
		return 1
	}
	return 0
}

func NilConverts() int {
	if unsafe.Pointer(nil) == nil {
		return 1
	}
	return 0
}

func SameAddrEq() int {
	var n int
	if unsafe.Pointer(&n) == unsafe.Pointer(&n) {
		return 1
	}
	return 0
}

func DiffAddrNe() int {
	var a, b int
	if unsafe.Pointer(&a) != unsafe.Pointer(&b) {
		return 1
	}
	return 0
}

// ---- conversions back to *T ----

func RoundTripRead() int {
	n := 7
	p := unsafe.Pointer(&n)
	return *(*int)(p)
}

func RoundTripWrite() int {
	var n int
	p := unsafe.Pointer(&n)
	*(*int)(p) = 42
	return n
}

func FieldAddrWrite() int {
	type S struct{ X int }
	var s S
	p := unsafe.Pointer(&s.X)
	*(*int)(p) = 9
	return s.X
}

func IndexAddrWrite() int {
	s := []int{1, 2, 3}
	p := unsafe.Pointer(&s[1])
	*(*int)(p) = 8
	return s[0]*100 + s[1]*10 + s[2] // 183
}

func TypedNilRoundTrip() int {
	p := unsafe.Pointer((*int)(nil))
	q := (*int)(p)
	if q == nil {
		return 1
	}
	return 0
}

func NilToOtherPtr() int {
	var p unsafe.Pointer
	q := (*byte)(p)
	if q == nil {
		return 1
	}
	return 0
}

// ---- declared shapes: fields, params, returns (text/template UPI) ----

type Data struct {
	UPI      unsafe.Pointer
	EmptyUPI unsafe.Pointer
}

func newUnsafePointer(n int) unsafe.Pointer {
	return unsafe.Pointer(&n)
}

func FieldRoundTrip() int {
	d := &Data{UPI: newUnsafePointer(21)}
	if d.UPI == nil || d.EmptyUPI != nil {
		return 0
	}
	return *(*int)(d.UPI) * 2 // 42
}

func ParamSlot(v unsafe.Pointer) int {
	if v == nil {
		return 0
	}
	return *(*int)(v) + 1
}

func ArgPasses() int {
	var n int
	n = 4
	return ParamSlot(unsafe.Pointer(&n)) // 5
}

func SliceElem() int {
	xs := []unsafe.Pointer{unsafe.Pointer(nil)}
	var n int
	xs = append(xs, unsafe.Pointer(&n))
	if xs[0] != nil || xs[1] == nil {
		return 0
	}
	return len(xs) // 2
}

// ---- type identity and assertions ----

func AssertSelf() int {
	var n int
	var i any = unsafe.Pointer(&n)
	if _, ok := i.(unsafe.Pointer); ok {
		return 1
	}
	return 0
}

func AssertOtherFails() int {
	var n int
	var i any = unsafe.Pointer(&n)
	if _, ok := i.(*int); ok {
		return 0
	}
	return 1
}

func TypeName() string {
	var n int
	return fmt.Sprintf("%T", unsafe.Pointer(&n))
}

func TypeSwitch() int {
	var n int
	var i any = unsafe.Pointer(&n)
	switch i.(type) {
	case unsafe.Pointer:
		return 1
	default:
		return 0
	}
}

// ---- aliases and declared types over unsafe.Pointer ----

type P = unsafe.Pointer

func AliasConvert() int {
	var n int
	n = 3
	return *(*int)(P(&n))
}

type Mine unsafe.Pointer

func NamedConvert() int {
	var n int
	n = 6
	p := Mine(&n)
	return *(*int)(unsafe.Pointer(p))
}

// ---- uintptr minting (one direction only) ----

func AddrNonZero() int {
	var n int
	a := uintptr(unsafe.Pointer(&n))
	if a == 0 {
		return 0
	}
	return 1
}

func AddrStable() int {
	var n int
	if uintptr(unsafe.Pointer(&n)) == uintptr(unsafe.Pointer(&n)) {
		return 1
	}
	return 0
}

// ---- traps: shapes gc rejects outright ----

func DerefUnsafe() int {
	var n int
	p := unsafe.Pointer(&n)
	return *p // gc: cannot indirect u
}

func AssignRawPtr() int {
	var n int
	var p unsafe.Pointer = &n // gc: cannot use &n as unsafe.Pointer
	_ = p
	return 0
}

func FieldRawPtr() int {
	var n int
	d := Data{UPI: &n} // gc: cannot use &n (type *int) as unsafe.Pointer
	_ = d
	return 0
}

func SliceConv() int {
	s := []int{1}
	_ = unsafe.Pointer(s) // gc: cannot convert s to unsafe.Pointer
	return 0
}

func FuncConv() int {
	f := func() {}
	_ = unsafe.Pointer(f) // gc: cannot convert f to unsafe.Pointer
	return 0
}

func NilDerefBack() int {
	p := unsafe.Pointer(nil)
	return *(*int)(p) // nil pointer dereference
}

func UintptrToPtr() int {
	var n int
	a := uintptr(unsafe.Pointer(&n))
	_ = unsafe.Pointer(a) // minigo boundary: no ref can be minted from an integer
	return 0
}
