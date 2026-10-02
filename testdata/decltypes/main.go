package main

import "io"

// Declared types are carried, not erased: `var x T` yields a typed zero,
// typed nils keep their declared type through ==, methods and asserts,
// and an interface slot holding a typed nil is non-nil (Go semantics).

type Sq struct{ Side int }

func (s Sq) Area() int { return s.Side * s.Side }

type PtrM struct{ N int }

// Safe is a pointer-receiver method that tolerates a nil receiver.
func (p *PtrM) Safe() int {
	if p == nil {
		return -1
	}
	return p.N
}

// ZeroStruct: var s Sq materializes a zero struct, not nil.
func ZeroStruct() int {
	var s Sq
	return s.Side + s.Area() // 0
}

// PkgLevelTypedZero: package-level `var x T` gets the same typed zero.
var pkgSq Sq

func PkgLevelTypedZero() int { return pkgSq.Side + 1 } // 1

// TypedNils: var xs []T / var m map[..] / var c chan are typed nils.
func TypedNils() int {
	var xs []int
	var m map[string]int
	var c chan int
	n := 0
	if xs == nil {
		n++
	}
	if m == nil {
		n++
	}
	if c == nil {
		n++
	}
	if len(xs)+len(m) != 0 {
		return -1
	}
	xs = append(xs, 3)
	return n*100 + len(xs) + xs[0] // 303
}

// NilOps: reads and writes on typed nils behave like Go.
func NilOps() int {
	var m map[string]int
	if v, ok := m["k"]; ok || v != 0 {
		return -1 // read on nil map: zero,false
	}
	delete(m, "k") // delete on nil map: no-op
	var xs []int
	n := 0
	for range xs {
		n++ // range over nil slice: zero iterations
	}
	return n + 1 // 1
}

// PtrNil: var p *T is a typed nil; p == nil and p.M() with a
// nil-tolerant pointer receiver both work.
func PtrNil() int {
	var p *PtrM
	if p != nil {
		return -1
	}
	if p.Safe() != -1 {
		return -2 // nil receiver dispatch
	}
	var q *Sq
	if q != nil {
		return -3
	}
	return 1
}

// IfaceNilBoxes: a typed nil crossing into an interface slot stays
// non-nil — `var x any = (*int)(nil)` behaves like Go.
func IfaceNilBoxes() int {
	var x any = (*int)(nil)
	if x == nil {
		return -1 // typed nil in an interface is NOT nil
	}
	if v, ok := x.(*int); !ok || v != nil {
		return -2 // assert unboxes to the typed nil: v == nil
	}
	return 1
}

// FuncNil: var f func() int is a typed nil; comparing and reading it is
// fine (calling it would panic like Go).
func FuncNil() int {
	var f func() int
	if f != nil {
		return -1
	}
	return 1
}

// LocalType: `type` declarations inside function bodies bind typedefs.
func LocalType() int {
	type P struct{ X int }
	p := P{X: 5}
	var q P
	return p.X + q.X // 5
}

// LocalTypeAssert: a local typedef also serves as an assert target.
func LocalTypeAssert() int {
	type P struct{ X int }
	var x any = P{X: 3}
	v, ok := x.(P)
	if !ok {
		return -1
	}
	return v.X // 3
}

// PtrElems: []T* literal elements auto-take their address like Go.
func PtrElems() int {
	ps := []*Sq{{Side: 3}, {Side: 4}}
	return ps[0].Side + ps[1].Side + ps[0].Area() // 3+4+9=16
}

// NamedPtrLit: named pointer types build the same way.
type NamedPtr *Sq

func NamedPtrVar() int {
	var p NamedPtr
	if p != nil {
		return -1
	}
	return 1
}

// MapKeyIdent: identifier keys in map literals are real expressions.
func MapKeyIdent() int {
	K := 2
	m := map[int]int{K: 10}
	return m[2] // 10
}

// ArrKeyIdent: identifier keys in array literals index, not field names.
func ArrKeyIdent() int {
	I := 1
	xs := []int{I: 7, 3: 9}
	return xs[1] + xs[3] // 16
}

// NamedReturnNil: `func f() (r *T)` bare return yields a typed nil.
func namedNil() (r *Sq) { return }

func NamedReturnNil() int {
	r := namedNil()
	if r != nil {
		return -1
	}
	return 1
}

// ReturnNilTyped: `return nil` under a *T result yields a typed nil.
func explicitNil() *Sq { return nil }

func ReturnNilTyped() int {
	if explicitNil() != nil {
		return -1
	}
	return 1
}

// ParamIfaceBox: passing a typed nil into an interface-typed param
// boxes it — the param is non-nil inside.
func holdsNil(x any) int {
	if x == nil {
		return -1
	}
	return 1
}

func ParamIfaceBox() int {
	var p *int
	return holdsNil(p) // 1: (*int)(nil) in any is non-nil
}

// InferCalls: call-site inference binds T/U from argument types.
func Id2[T any](v T) T          { return v }
func Fst2[T, U any](a T, b U) T { return a }

func InferCalls() int {
	return Id2(40) + Fst2(2, "x") // 42
}

// GenericZero: `var z T` inside a generic body yields T's zero value,
// and a typed-nil T returned as any keeps its type (non-nil interface).
func ZeroT[T any]() any {
	var z T
	return z
}

func GenericZero() int {
	z := ZeroT[int]()
	if z != 0 {
		return -1
	}
	zs := ZeroT[[]int]()
	if zs == nil {
		return -2 // any holding []int(nil) is a non-nil interface
	}
	return 1
}

// ConstraintOK: a satisfied constraint instantiates normally.
func TakesNum[T ~int | ~string](v T) T { return v }

func ConstraintOK() int {
	return TakesNum[int](40) + 2 // 42
}

// ConstraintBad: bool does not satisfy ~int|~string — the instantiation
// itself traps, like the Go type checker rejecting the program.
func ConstraintBad() int {
	_ = TakesNum[bool](true)
	return 0
}

// --- review fixes ---

// SliceElemBox: literal elements coerce to the element type — []any
// boxes a typed nil (non-nil), []*int keeps it (nil).
func SliceElemBox() int {
	as := []any{(*int)(nil)}
	if as[0] == nil {
		return -1
	}
	ps := []*int{nil}
	if ps[0] != nil {
		return -2
	}
	return 1
}

// SliceAlias: `var y any = xs` must not rewrite xs's elements — the
// caller's slice keeps its concrete element type.
func SliceAlias() int {
	xs := []*int{nil}
	var y any = xs
	_ = y
	if xs[0] != nil {
		return -1 // boxing the slice must not unbox its elements
	}
	return 1
}

// VarargZero: a missing variadic rest binds []T(nil), not T's zero.
func VarargZero() int {
	return varargLen() + varargLen(1, 2, 3)
}

func varargLen(xs ...int) int {
	return len(xs) + len(append(xs, 1))
}

// MapMissZero: m[k] for a missing key (or nil map) yields the element
// type's zero, not an untyped nil.
func MapMissZero() int {
	m := make(map[string]int)
	v := m["nope"]
	if v != 0 {
		return -1
	}
	var nm map[string]int
	v2, ok := nm["nope"]
	if ok || v2 != 0 {
		return -2
	}
	return 1
}

// NamedZero: named scalar types zero as their underlying literal.
type Str string
type Boo bool

func NamedZero() int {
	var s Str
	var b Boo
	if s+Str("x") != "x" {
		return -1
	}
	if b {
		return -2
	}
	return 1
}

// MultiReturnBox: `return pair()` under (any, int) results boxes the
// first element into the interface slot (typed nil -> non-nil).
func innerPair() (*int, int) { return nil, 3 }
func outerPair() (any, int)  { return innerPair() }

func MultiReturnBox() int {
	a, b := outerPair()
	if a == nil || b != 3 {
		return -1
	}
	return 1
}

// --- typed-zero follow-ups (round 6) ---

// NamedIdent: a declared named-basic value carries its declared type, and
// plain assignment into the typed slot keeps it (`c = v` checks like
// `var c T = v`). Methods on the named type dispatch through the tag.
type Celsius float64

func (c Celsius) F() float64 { return float64(c)*9/5 + 32 }

func NamedIdent() float64 {
	var c Celsius
	c = 36.5
	return c.F() // 36.5*9/5+32 = 97.7
}

// NamedOps: arithmetic on named basics keeps the declared tag — an
// untyped operand adopts the named type and the result stays named.
func (s Str) Loud() Str { return s + "!" }

func NamedOps() string {
	var s Str = "a"
	t := s + Str("b")
	if t != Str("ab") {
		return "concat mismatch"
	}
	return string(s.Loud()) // "a!" converted out to a plain string
}

// NamedStore: assignment into a declared-typed slot re-tags the value —
// x = v behaves like `var x T = v` for named types too.
func NamedStore() int {
	var c Celsius = 1
	c = 41
	c = c + 1 // 42, still Celsius
	var x any = c
	if _, ok := x.(Celsius); !ok {
		return -1
	}
	return int(c) // 42
}

// NamedAssert: a named basic value inside any asserts back to its
// declared type — the underlying builtin name does NOT match.
func NamedAssert() int {
	var x any = Celsius(40)
	if _, ok := x.(float64); ok {
		return -1 // Celsius is not float64, like Go
	}
	c, ok := x.(Celsius)
	if !ok {
		return -2
	}
	return int(c / 10) // 4
}

// NoInheritBad: `type A B` shares B's storage, not B's methods — the
// method select traps like Go's "a.Loud undefined".
type StrAlias Str

func NoInheritBad() int {
	var a StrAlias = StrAlias("x")
	return len(a.Loud())
}

// MapBindZero: a declared map type stamps the map value on binding, so
// missing-key reads yield the declared element zero even for maps that
// were built by a bare literal elsewhere.
type M2 map[string]int

func MapBindZero() int {
	var m M2 = map[string]int{"a": 1}
	if m["missing"] != 0 {
		return -1 // m carries M2 -> missing key is int's zero
	}
	var m2 M2 = m
	if v, ok := m2["missing"]; ok || v != 0 {
		return -2
	}
	var nilM M2
	if nilM["k"] != 0 {
		return -3 // nil M2 reads through its declared element type
	}
	return m["a"]
}

// MapBindTrap: a named map type where a differently-shaped declared map
// binds is a type error (the shape check runs through the underlying).
type M3 map[string]float64

func MapBindTrap() int {
	var m M2 = map[string]int{}
	var o M3 = m // M3's element is float64 — map[string]int does not fit
	_ = o
	return 0
}

// FieldHoleZero: a field whose declared type does not resolve (missing
// import, unbound name) keeps a typed-nil hole inside an otherwise typed
// struct zero — boxed into an interface it is non-nil, like a typed nil.
type Holder struct {
	R io.NotAReader // the member is deliberately absent from io
}

func FieldHoleZero() int {
	var h Holder
	var x any = h.R
	if x == nil {
		return -1 // a typed-nil hole keeps its dynamic type — non-nil
	}
	if h.R != nil {
		return -2 // but it still compares nil like a nil interface member
	}
	return 1
}

// AssignOK: legal `var x T = v` binds pass the check — literals adopt
// declared types and interface satisfaction is enforced.
type IArea interface{ Area() int }

func AssignOK() int {
	var i int = 41
	i = i + 1
	var sh IArea = Sq{Side: 2}
	if sh.Area() != 4 {
		return -1
	}
	var s Str = "hi"
	if s != "hi" {
		return -2
	}
	return i // 42
}

// AssignBadInt: a string literal cannot bind an int var.
func AssignBadInt() int {
	var i int = "nope"
	return i
}

// AssignBadNamed: a named basic value cannot bind a different builtin —
// `var i int = c` needs a conversion in Go.
func AssignBadNamed() int {
	var c Celsius = 1
	var i int = c
	return i
}

// AssignMix: two different declared types refuse to mix in one op.
func AssignMix() int {
	var a Celsius = 1
	var b Str = "x"
	_ = a + b // mismatched types Celsius and Str
	return 0
}

// IfaceBad: a value without the method set cannot bind an interface var.
func IfaceBad() int {
	var sh IArea = 42
	_ = sh
	return 0
}

// NilBadShape: a typed nil retags only through matching shapes —
// (*Sq)(nil) does not fit a *int slot.
func NilBadShape() int {
	var p *int = (*Sq)(nil)
	_ = p
	return 0
}

// --- review fixes (round 6, Devin Review) ---

// IfaceNilOK: a typed nil whose type satisfies the interface still boxes.
func IfaceNilOK() int {
	var e IArea = (*Sq)(nil)
	_ = e
	return 1
}

// IfaceNilBad: a typed nil whose type lacks the method set cannot bind a
// method-requiring interface — (*int)(nil) is not an IArea.
func IfaceNilBad() int {
	var e IArea = (*int)(nil)
	_ = e
	return 0
}

// NamedPtrIface: &t of a named basic satisfies an interface through the
// named type's method set — the pointer must not deref past the tag.
type TempC float64

func (t *TempC) Bump() { *t = *t + 1 }

type Bumper interface{ Bump() }

func NamedPtrIface() int {
	var t TempC = 5
	var w Bumper = &t // *TempC's method set includes Bump
	w.Bump()
	return int(t) // 6
}

// NamedUnary: -x, +x, ^x, !x keep the operand's declared type.
func NamedUnary() int {
	var c Celsius = 40
	var n any = -c // still Celsius
	if _, ok := n.(Celsius); !ok {
		return -1
	}
	var b Boo = true
	var n2 any = !b // still Boo
	if _, ok := n2.(Boo); !ok {
		return -2
	}
	return int(-c / 10) // -4
}

// ChainHoleBad: `type A B` does not accept a B value — both are named
// types (Go needs a conversion) even when B's own underlying cannot be
// resolved further.
type BHole io.NotAReader // unresolvable: the member is absent from io
type AHole BHole

func ChainHoleBad() int {
	var b BHole = "x" // loose: unresolvable underlying passes unchecked
	var a AHole = b   // cannot use BHole as AHole
	_ = a
	return 0
}

// AliasBindOK: `type A = B` is the same type — a B value binds an A slot
// and keeps its (shared) declared identity.
type AEq = Str

func AliasBindOK() int {
	var b Str = "x"
	var a AEq = b
	if a != "x" {
		return -1
	}
	return 1
}

// StructNamedBad: `type A Sq` does not accept a Sq value — both sides
// are named types even though the storage is shared.
type SqA Sq
type SqO Sq

func StructNamedBad() int {
	var s Sq
	var a SqA = s // cannot use Sq as SqA
	_ = a
	return 0
}

// StructAliasOK: `type A = Sq` binds a Sq value — aliases are the same
// type, so the bind keeps the shared declared identity.
type SqAlias = Sq

func StructAliasOK() int {
	var s Sq
	s.Side = 3
	var a SqAlias = s
	return a.Side // 3
}

// AnonStructOK: an unnamed struct literal binds a named struct type
// when the field sets match (unnamed -> named is assignable in Go).
func AnonStructOK() int {
	var a SqA = struct{ Side int }{Side: 4}
	return a.Side // 4
}

// PtrElemBad / PtrElemOK: a named pointer binds only its own pointee
// type (`var p P = &v` checks v's declared type against P's element).
type PSq *Sq

func PtrElemBad() int {
	var o SqO
	var p PSq = &o // cannot use *SqO as PSq
	_ = p
	return 0
}

func PtrElemOK() int {
	var s Sq
	var p PSq = &s
	p.Side = 7
	return s.Side // 7 — &s aliases s's own cell
}

// BoxStoreOK / BoxStoreBad: `*p = v` through a composite-literal box
// coerces to the pointee's declared type like a `var x T` store.
func BoxStoreOK() int {
	p := &Sq{Side: 1}
	*p = Sq{Side: 5}
	return p.Side // 5
}

func BoxStoreBad() int {
	p := &Sq{Side: 1}
	var o SqO
	*p = o // cannot use SqO as Sq
	return 0
}

// MapRebindBad: two different named map types do not re-bind.
type M4 map[string]int

func MapRebindBad() int {
	var m2 M2 = M2{"a": 1}
	var m4 M4 = m2 // cannot use M2 as M4
	_ = m4
	return 0
}

// MapAliasOK: an alias binds the same named map (identity, not shape).
type M2A = M2

func MapAliasOK() int {
	var m2 M2 = M2{"a": 9}
	var a M2A = m2
	return a["a"] // 9
}

// SliceRebindBad / SliceElemTyped: named slices tag like named maps —
// rebinds check identity and element stores coerce to the element type.
type S3 []int
type S4 []int
type EI int
type SE []EI

func SliceRebindBad() int {
	var s3 S3 = S3{1}
	var s4 S4 = s3 // cannot use S3 as S4
	_ = s4
	return 0
}

func SliceElemTyped() int {
	var s SE = SE{EI(1)}
	s[0] = EI(7)
	var a any = s[0]
	if v, ok := a.(EI); !ok {
		return -1
	} else {
		return int(v) // 7
	}
}

// ChanElemTyped: a send coerces to the channel's declared element type.
type CE chan EI

func ChanElemTyped() int {
	var ch CE = make(CE, 1)
	ch <- EI(3)
	x := <-ch
	var a any = x
	v, ok := a.(EI)
	if !ok {
		return -1
	}
	return int(v) // 3
}

// ConstTyped: a typed const keeps its declared type (`const k T = v`
// coerces like `var x T = v`), at package level and locally.
const KC Celsius = 40

func ConstTyped() int {
	var a any = KC
	if _, ok := a.(Celsius); !ok {
		return -1
	}
	const k Str = "hi"
	var b any = k
	if _, ok := b.(Str); !ok {
		return -2
	}
	return int(KC/10) + len(k) // 6
}

func main() {}
