package main

// Regressions found by the language-surface fuzz experiment
// (docs/sketch/ja/fuzz-language.md): each function pins one fixed bug.

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/template"
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

// a conversion-produced sized int keeps the tag through arithmetic:
// -uint8(5) is 251 and u-10 wraps, not just on declared vars.
func ConvSizedInt() string {
	u := uint8(5)
	return fmt.Sprintf("%v %v %T", -u, u-10, u)
}

// constant expressions fold in the exact domain: 1<<100>>50 is 2^50,
// not the 0 an int64 runtime shift would produce.
func ConstFoldShift() int64 {
	return 1 << 100 >> 50
}

// 1/0 as a constant expression traps at compile, matching Go's
// division-by-zero compile error — not a runtime panic.
func ConstDivZero() int {
	return 1 / 0
}

// ---- use-case-fuzz leftovers (PR-30; docs/sketch/ja/fuzz-usecase.md) ----

// unsigned-domain ops: >> on uint fills zeros, ^ and - stay unsigned,
// and %/ use unsigned division — `uint8(200)/uint8(7)` is 28 not -3.
func UnsignedOps() string {
	var u uint64 = 1 << 60
	w := u >> 65   // shift >= width -> 0
	x := ^uint8(5) // 250
	d := uint8(200) / uint8(7)
	m := uint8(200) % uint8(7)
	return fmt.Sprintf("%v %v %v %v %v", u, w, x, d, m)
}

// shifting by an unsigned-typed count uses its raw bit pattern:
// `x << uint(-4)` sees a huge count -> 0.
func ShiftUintCount() string {
	var x int64 = 1
	c := uint(4) - 8 // 18446744073709551612
	return fmt.Sprintf("%v %v", x<<c, x>>uint64(70))
}

// a negative shift count panics like Go (recoverable runtime error).
func ShiftNegCount() (r string) {
	defer func() {
		if v := recover(); v != nil {
			r = fmt.Sprintf("%v", v)
		}
	}()
	var x int64 = 1
	return fmt.Sprintf("%v", x>>int64(-1))
}

// (*T)(p) conversion: a pointer conversion re-tags the same cell so
// *sp reads and writes as T while *p keeps the base type.
type SV string

func (s *SV) Set(v string) { *s = SV(v) }

func PtrConv() string {
	s := "x"
	p := &s
	sp := (*SV)(p)
	sp.Set("y")
	return fmt.Sprintf("%v %T %T", s, *sp, *p)
}

// (*T)(p) shares the pointee's address: storing through *p is
// observable through *sp.
func PtrConvShared() string {
	s := "a"
	sp := (*SV)(&s)
	*sp = "b" // store through the converted pointer
	return s
}

// an untyped constant lands already converted to its destination type:
// `var f float64 = 3` divides as a float, and a sized decl tags so %T
// spells the declared width (float32, int64 — also the zero value).
type CF float64
type CI8 int8

func ConstDestType() string {
	var f32 float32 = 1.5
	var x CF = 3
	var i64 int64
	const cf float64 = 3
	var i int = 7
	var i8 CI8 = 100
	return fmt.Sprintf("%v %v %v|%T %T %T %T %T", x/2, cf/2, f32/2, f32, x, i64, i, i8)
}

// declared slice/map/chan typedefs keep their methods when checked
// against an interface — a conversion `B("x")` must satisfy W.
type B []byte

func (b B) Count() int { return len(b) }

type W interface{ Count() int }

var _ W = B{} // package-level static assert

func SliceIfaceMethod() string {
	var w W = B("abcd")
	return fmt.Sprintf("%d", w.Count())
}

// chan any carries a container verbatim — the map sent is the map
// received, so mutation through the received alias is visible.
func ChanAnyMap() string {
	m := map[string]int{"a": 1}
	ch := make(chan any, 1)
	ch <- m
	got := <-ch
	got.(map[string]int)["a"] = 8
	return fmt.Sprintf("%d", m["a"])
}

// chan any keeps a slice's identity too.
func ChanAnySlice() string {
	s := []int{1, 2, 3}
	ch := make(chan any, 1)
	ch <- s
	got := <-ch
	return fmt.Sprintf("%v", got)
}

// io.ReadAll marshals through a host reader.
func IoReadAll() string {
	b, err := io.ReadAll(strings.NewReader("payload"))
	return fmt.Sprintf("%s %v", b, err)
}

// io.ReadFull writes back into the script's byte slice and returns
// the real io.EOF sentinel on a short read.
func IoReadFullEOF() string {
	r := strings.NewReader("")
	buf := make([]byte, 4)
	n, err := io.ReadFull(r, buf)
	if err != io.EOF {
		return fmt.Sprintf("unexpected err %v", err)
	}
	return fmt.Sprintf("%d %q", n, buf)
}

// io.Copy from a host reader to a host writer.
func IoCopy() string {
	var sb strings.Builder
	n, err := io.Copy(&sb, strings.NewReader("xy"))
	return fmt.Sprintf("%d %v %v", n, sb.String(), err)
}

// crypto/sha256 native bind: Sum256 and New/Write produce the digest.
func Sha256Bind() string {
	sum := sha256.Sum256([]byte("hello"))
	h := sha256.New()
	h.Write([]byte("hello"))
	return fmt.Sprintf("%x %x", sum[:4], h.Sum(nil)[:4])
}

// encoding/csv round-trip through host reader/writer.
func CsvBind() string {
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	w.Write([]string{"a", "b"})
	w.Flush()
	r := csv.NewReader(strings.NewReader(sb.String()))
	rec, err := r.ReadAll()
	return fmt.Sprintf("%v %v", rec, err)
}

// bufio.Scanner over a script-side multi-line string.
func BufioBind() string {
	sc := bufio.NewScanner(strings.NewReader("a\nb"))
	var got []string
	for sc.Scan() {
		got = append(got, sc.Text())
	}
	return fmt.Sprintf("%v %v", got, sc.Err())
}

// text/template executes against a host strings.Builder.
func TemplateBind() string {
	t := template.Must(template.New("t").Parse("hi {{.}}"))
	var sb strings.Builder
	err := t.Execute(&sb, "ann")
	return fmt.Sprintf("%s %v", sb.String(), err)
}

// a bodiless declaration (asm stub, //go:linkname target) compiles to
// a no-op returning its declared zero values.
func Bodiless() int

func BodilessCall() int { return Bodiless() + 3 }

// os.Args reflects the engine's script args (WithArgs / `minigo run --`).
func OsArgs() string { return strings.Join(os.Args, ",") }
