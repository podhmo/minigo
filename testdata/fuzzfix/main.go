package main

// Regressions found by the language-surface fuzz experiment
// (docs/sketch/ja/fuzz-language.md): each function pins one fixed bug.

import (
	"errors"
	"fmt"
)

// elided-key struct literals in maps must key by content, not identity.
type K struct{ X, Y int }

func MapElidedKey() string {
	m := map[K]string{{1, 2}: "x", {3, 4}: "y"}
	return m[K{1, 2}]
}

// array keys hash by content; a slice key must panic, not corrupt.
func MapArrayKey() int {
	m := map[[2]int]int{}
	m[[2]int{1, 2}] = 9
	return m[[2]int{1, 2}]
}

// anonymous struct literals compare equal field-wise.
func AnonStructEq() bool {
	return struct{ A int }{A: 1} == struct{ A int }{1}
}

// a[:] on an array yields a slice sharing the backing — writes alias.
func ArraySliceShare() int {
	a := [4]int{1, 2, 3, 4}
	s := a[:]
	s[0] = 99
	return a[0]
}

// cap(a[1:3]) of a 4-array is 3.
func ArraySliceCap() int {
	a := [4]int{1, 2, 3, 4}
	return cap(a[1:3])
}

// `b := a` on an array deep-copies — b writes must not touch a.
func ArrayCopy() int {
	a := [2]int{1, 2}
	b := a
	b[0] = 99
	return a[0]
}

// []int → [3]int / *[3]int conversions.
func SliceToArray() string {
	s := []int{1, 2, 3}
	a := [3]int(s)
	p := (*[3]int)(s)
	p[0] = 7
	return fmt.Sprintf("%v %v", a, p[0])
}

// -x on a sized int keeps the width: -uint8(5) is 251.
func UnarySizedInt() int64 {
	var x uint8 = 5
	return int64(-x)
}

// compound assign through a parenthesized pointer: (*p) += 10.
func ParenCompoundAssign() int {
	x := 1
	p := &x
	(*p) += 10
	(*p)++
	return x
}

// x &^= y parses as AND_NOT assign, not AND + NOT.
func AndNotAssign() int64 {
	x := int64(0b1100)
	x &^= 0b0100
	return x
}

// copy(dst, "str") into []byte.
func CopyFromString() string {
	b := make([]byte, 3)
	n := copy(b, "hello")
	return fmt.Sprintf("%d %q", n, string(b))
}

// a typed-nil receiver through an interface calls the method.
type NP struct{ X int }

func (p *NP) M() int {
	if p == nil {
		return -1
	}
	return p.X
}

func IfaceTypedNilMethod() int {
	var i interface{ M() int } = (*NP)(nil)
	return i.M()
}

// method expression on a named pointer type: (*T).M
type CT struct{ N int }

func (c *CT) Bump() { c.N++ }

func MethodExprPtr() int {
	f := (*CT).Bump
	c := &CT{N: 4}
	f(c)
	return c.N
}

// runtime-error messages match Go's shape.
func NilSliceIndexMsg() (r string) {
	defer func() {
		r = fmt.Sprintf("%v", recover())
	}()
	var s []int
	_ = s[0]
	return "unreached"
}

// call of nil function value panics like a nil deref.
func CallNilFuncMsg() (r string) {
	var f func() int
	defer func() {
		r = fmt.Sprintf("%v", recover())
	}()
	_ = f()
	return "unreached"
}

// failed type assertion names both sides.
func AssertMissMsg() (r string) {
	var i any = "s"
	defer func() {
		r = fmt.Sprintf("%v", recover())
	}()
	_ = i.(int)
	return "unreached"
}

// fmt verbs: %T spells the script type, %q quotes []byte, %c descends
// elementwise, %b/%x work on composites.
func FmtVerbs() string {
	b := []byte("hel")
	return fmt.Sprintf("%q %T %T %c",
		b, []int{1}, map[string]int{}, 'A')
}

// %T on a named decl prints pkg-qualified.
func FmtPkgType() string {
	return fmt.Sprintf("%T %#v", K{}, K{1, 2})
}

// package-var init order is dependency-driven, not text order.
var log = ""

func track(s string) string { log += s; return s }

var ivA = track("a") + ivB
var ivB = track("b")
var ivC = track("c")

func InitDepOrder() string { return log }

// errors: %w wraps, Unwalk chain is visible to Unwrap/Is/As.
type MyErr struct{ Code int }

func (e *MyErr) Error() string { return fmt.Sprintf("myerr%d", e.Code) }

func ErrorfWrap() string {
	e1 := errors.New("x")
	err := fmt.Errorf("o: %w", e1)
	u := errors.Unwrap(err)
	var me *MyErr
	as := errors.As(fmt.Errorf("o2: %w", &MyErr{Code: 7}), &me)
	return fmt.Sprintf("%v %v %v %d", err, u, as, me.Code)
}

// panic(nil) recovers to *runtime.PanicNilError.
func PanicNilType() (r string) {
	defer func() {
		v := recover()
		r = fmt.Sprintf("%T %v", v, v)
	}()
	panic(nil)
}

// a named basic type's unary minus keeps the tag through Named wrap.
type MyU8 uint8

func NamedUnary() string {
	var x MyU8 = 5
	return fmt.Sprintf("%v", -x)
}
