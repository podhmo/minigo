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

	"github.com/podhmo/minigo/testdata/errfooa"
	"github.com/podhmo/minigo/testdata/errfoob"
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

// an integer/integer quotient of constants is an integer constant:
// 7/2 folds to 3, not the rational 3.5 go/constant's QUO computes.
func ConstIntDiv() int {
	return 7/2 + 10/3 - -7/2
}

// a constant-zero divisor traps only when the whole expression is
// constant: a variable dividend divides at runtime — floats give
// ±Inf or NaN, never a compile trap.
func RuntimeFloatDivZero() string {
	x, z := 1.0, 0.0
	return fmt.Sprintf("%v %v %v", x/z, -x/z, z/z)
}

// int / 0 with a variable dividend is a recoverable runtime panic
// like Go's, not a compile-time trap recover() cannot see.
func RuntimeIntDivZero() (r string) {
	defer func() {
		r = fmt.Sprintf("%v", recover())
	}()
	x, z := 7, 0
	return fmt.Sprintf("%v", x/z)
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
// `x << ^uint(3)` sees a huge count -> 0. (gc rejects `uint(4) - 8`
// as a constant expression, so the count comes from ^ instead.)
func ShiftUintCount() string {
	var x int64 = 1
	c := ^uint(3) // 18446744073709551612
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

// io.ReadFull commits the read bytes back into the script's byte
// slice — a borrowed buffer that never writes back leaves zeros (#362).
func IoReadFullFill() string {
	r := strings.NewReader("abcdefgh")
	buf := make([]byte, 8)
	n, err := io.ReadFull(r, buf)
	return fmt.Sprintf("%d %q %v", n, buf, err)
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

// errors.As must not match a same-named error type from another package:
// a *errfooa.Same chain element does not satisfy a *errfoob.Same target.
func ErrAsCrossPkg() string {
	var a *errfooa.Same
	var b *errfoob.Same
	err := fmt.Errorf("wrap: %w", &errfooa.Same{N: 1})
	hitA := errors.As(err, &a)
	hitB := errors.As(err, &b)
	return fmt.Sprintf("%v %v", hitA, hitB)
}

// a 3-index slice caps the result's capacity the way Go does.
func ThreeIndexSlice() string {
	s := []int{1, 2, 3, 4, 5}[1:3:4]
	return fmt.Sprintf("%v %d %d", s, len(s), cap(s))
}

// float32 slots narrow stored values and arithmetic results:
// float64(float32(0.1)) is 0.10000000149011612, and %v prints the
// float32 digits back.
func Float32Narrow() string {
	var f float32 = 0.1
	var g float32 = 0.2
	var arr [1]float32
	arr[0] = 0.1
	return fmt.Sprintf("%v %v %v %T", f+g, arr[0], f, f)
}

// assert failures name the static interface type (main.asI), the dynamic
// concrete type, and the target — like the gc panic.
type asI interface{ AM() }
type asT struct{}

func (asT) AM() {}

type asT2 struct{}

func (asT2) AM() {}

func AssertStaticName() (r string) {
	var i asI = asT{}
	defer func() { r = fmt.Sprintf("%v", recover()) }()
	_ = i.(asT2)
	return
}

// a failed interface-to-interface assert names the missing method, and a
// boxed host value names its Go type.
func AssertMissingMethod() (r string) {
	defer func() { r = fmt.Sprintf("%v", recover()) }()
	var a any = errors.New("x")
	_ = a.(io.Writer)
	return
}

// []byte and []rune element reads carry the element type (uint8/int32),
// not a bare int.
func ByteRuneElemTyp() string {
	b := []byte("abc")
	r := []rune("xyz")
	s := "s"
	return fmt.Sprintf("%T %T %T", b[0], r[0], s[0])
}

// ---- untyped constants + complex values (fuzz-round leftovers) ----

type ufRune rune
type ufC64 complex64

// a bare 'a' stays untyped until it binds: its default type is rune
// (int32), and rune-flavor propagates through constant expressions.
func UConstRuneDefault() string {
	var r = 'a'
	var y = 'a' + 1
	return fmt.Sprintf("%T %T %v", r, y, y)
}

// an over-wide integer constant compiles while unused and still folds
// exactly in constant expressions (`B - B` is 0, not an overflow).
func UConstBigConstExpr() string {
	const B = 1 << 100
	const C = B - (1 << 99)
	return fmt.Sprintf("%v %v", B-B, C == B-(1<<99))
}

// materializing the same constant as a value is Go's "overflows int"
// compile rejection — a trap here.
func UConstBigTrap() string {
	var x = 1 << 100
	return fmt.Sprint(x)
}

// a float literal past float64 range is legal until it must fit one.
func UConstFloatOverflow() string {
	var f = 1e500
	return fmt.Sprint(f)
}

// a rune constant converts into a declared numeric type directly.
func UConstNamedRune() string {
	var mr ufRune = 'a'
	var i8 int8 = 'a'
	return fmt.Sprintf("%T %v %v", mr, mr, i8)
}

// rune constants convert to string as the rune, and can sit in a
// []byte literal.
func UConstRuneConv() string {
	return fmt.Sprintf("%s %v", string('a'), []byte{'x', 'y'})
}

// %T names the materialized default type of a conversion target or a
// bare literal.
func PctTDefaults() string {
	return fmt.Sprintf("%T %T %T", int64(5), int8(5), 'a')
}

// complex values: complex/real/imag builtins, arithmetic, and %T.
func ComplexOps() string {
	var c64 complex64 = 1 + 2i
	var c128 = complex(3, 4)
	d := complex(1.5, 2.5)
	return fmt.Sprintf("%v %v %v %v %v %v %v %T %v",
		real(c64), imag(c64), real(c128), imag(c128),
		c64+c64, c128*c128, 1+2i+3i, d, d)
}

// a declared complex type tags through conversion and keeps its name.
func ComplexDecl() string {
	var mc ufC64 = 1 + 2i
	fmt.Println(complex64(complex(1, 2)) + complex64(1))
	return fmt.Sprintf("%T %v", mc, mc)
}

// complex keys hash by value.
func ComplexMapKey() string {
	m := map[complex128]int{complex(1, 2): 5}
	return fmt.Sprintf("%v %v", m[complex(1, 2)], m[complex(0, 0)])
}

// mixed complex widths are Go's mismatched-types rejection.
func ComplexMixedWidth() string {
	var c64 complex64 = 1
	var c128 complex128 = 1
	return fmt.Sprint(c64 != c128)
}

// ordered comparison on complex is rejected in Go.
func ComplexOrdered() string {
	c := complex(1, 2)
	return fmt.Sprint(c < c)
}
