package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/url"
	"path"
	"regexp"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

// Sprintf runs the real fmt.Sprintf via intrinsics — no GOROOT parse.
func Sprintf() string { return fmt.Sprintf("hi %d", 7) }

// StrconvAtoi exercises the (value, err) tuple convention.
func StrconvAtoi() int {
	n, err := strconv.Atoi("42")
	if err != nil {
		return -1
	}
	return n
}

// StringsJoin uses the native strings.Join.
func StringsJoin() string { return strings.Join([]string{"a", "b", "c"}, ",") }

// ErrorsNew wraps errors.New; Error() returns the message via method call.
func ErrorsNew() string {
	err := errors.New("oops")
	if err == nil {
		return "no error"
	}
	return err.Error()
}

// SortIntsInPlace: sort.Ints mutates the caller's slice, not a copy.
func SortIntsInPlace() int {
	s := []int{3, 1, 2}
	sort.Ints(s)
	return s[0]*100 + s[1]*10 + s[2] // 123
}

// SlicesSortInPlace: slices.Sort mutates the caller's slice.
func SlicesSortInPlace() string {
	s := []string{"b", "a", "c"}
	slices.Sort(s)
	return s[0] + s[1] + s[2] // "abc"
}

// Prints writes to the engine's configured output (WithOutput) — the
// assertion lives in the test, which captures the buffer.
func Prints() int {
	fmt.Println("hello", 42)
	return 0
}

// SortSearch finds the first index satisfying the predicate.
func SortSearch() int {
	return sort.Search(10, func(i int) bool { return i*i >= 30 }) // 6
}

// SortStableByLen: equal-length elements keep their input order.
func SortStableByLen() string {
	s := []string{"bb", "a", "cc", "d"}
	sort.SliceStable(s, func(i, j int) bool { return len(s[i]) < len(s[j]) })
	return s[0] + s[1] + s[2] + s[3] // "adbbcc" — bb stays before cc
}

// SortStableFuncByLen exercises slices.SortStableFunc with a script cmp.
func SortStableFuncByLen() string {
	s := []string{"bb", "a", "cc", "d"}
	slices.SortStableFunc(s, func(a, b string) int { return len(a) - len(b) })
	return s[0] + s[1] + s[2] + s[3] // "adbbcc"
}

// BinarySearchHit exercises the (index, found) tuple.
func BinarySearchHit() int {
	i, ok := slices.BinarySearch([]int{1, 3, 5, 7}, 5)
	if !ok {
		return -1
	}
	return i // 2
}

// BinarySearchMiss returns the insertion point when absent.
func BinarySearchMiss() int {
	i, ok := slices.BinarySearch([]int{1, 3, 5, 7}, 4)
	if ok {
		return -1
	}
	return i // 2
}

type myString string

// BinarySearchNamed finds elements in a slice of a named basic type:
// elements arrive as *Named wrappers, which the comparator must unwrap.
func BinarySearchNamed() int {
	i, ok := slices.BinarySearch([]myString{"a", "bb", "ccc"}, myString("bb"))
	if !ok {
		return -1
	}
	return i // 1
}

// BinarySearchFunc uses a script comparator.
func BinarySearchFunc() int {
	i, ok := slices.BinarySearchFunc([]string{"a", "bb", "ccc"}, "zzzz",
		func(x, t string) int { return len(x) - len(t) })
	if ok {
		return -1
	}
	return i // 3
}

// RuntimeGOOS reports the host GOOS via intrinsics — non-empty everywhere.
func RuntimeGOOS() bool { return goruntime.GOOS != "" }

// RuntimeGoroutines reports the real host goroutine count: goroutines
// spawned by `go` are actual host goroutines.
func RuntimeGoroutines() bool { return goruntime.NumGoroutine() >= 1 }

// RuntimeGOMAXPROCS is read-only on the script side: the argument is
// ignored and the host's current setting is returned (a script must not
// mutate the host process's parallelism).
func RuntimeGOMAXPROCS() int {
	before := goruntime.GOMAXPROCS(0)
	if goruntime.GOMAXPROCS(999) != before {
		return -1 // the "setter" changed something
	}
	return 1
}

// UnsafeSizeofInt approximates unsafe.Sizeof over the boxed value.
func UnsafeSizeofInt() int { return int(unsafe.Sizeof(int64(0))) } // 8

// UnsafeSizeofSlice reports the 3-word slice header approximation.
func UnsafeSizeofSlice() int { return int(unsafe.Sizeof([]int{})) } // 24

// UnsafeAlignofEmpty: alignment is at least 1 even for the empty struct.
func UnsafeAlignofEmpty() int { return int(unsafe.Alignof(struct{}{})) } // 1

// StringsSplitN exercises a member added past the original bound set.
func StringsSplitN() string { return strings.SplitN("a:b:c", ":", 2)[1] } // "b:c"

func StringsTrim() string { return strings.Trim("<<x>>", "<>") } // "x"

func StringsLastIndex() int { return strings.LastIndex("aXbXc", "X") } // 3

func StringsTitle() string { return strings.Title("hello world") } // "Hello World"

func StrconvFormatUint() string { return strconv.FormatUint(255, 16) } // "ff"

func StrconvIsPrint() bool { return strconv.IsPrint('a') && !strconv.IsPrint('\x00') }

// BytesBuffer is the Builder stand-in: a host *bytes.Buffer boxed as a
// GoValue, driven through reflective method dispatch.
func BytesBuffer() string {
	b := bytes.NewBufferString("x")
	b.WriteString("y")
	b.WriteByte('z')
	return b.String() // "xyz"
}

func BytesFields() string { return string(bytes.Fields([]byte(" a b "))[0]) } // "a"

func Utf8Count() int { return utf8.RuneCountInString("héllo") } // 5

// Utf8Encode: gc signature — writes into the caller's []byte and
// returns the byte count (the encoded rune is read back from p).
func Utf8Encode() string {
	var buf [utf8.UTFMax]byte
	n := utf8.EncodeRune(buf[:], '☺')
	return fmt.Sprintf("%d %s %v", n, buf[:n], buf) // "3 ☺ [226 152 186 0]"
}

func UnicodeDigit() bool {
	return unicode.IsDigit('3') && unicode.IsUpper('A') && !unicode.IsSpace('x')
}

func MathRound() bool {
	return math.Pow(2, 10) == 1024 && math.Abs(-3) == 3 && math.Min(2, 1) == 1 && math.MaxInt > 0 && math.Pi > 3.14
}

// RegexpReplace drives a host *regexp.Regexp through reflective dispatch.
func RegexpReplace() string {
	re := regexp.MustCompile("a+")
	return re.ReplaceAllString("baaaac", "X") // "bXc"
}

func RegexpMatch() bool {
	m, _ := regexp.MatchString("^h.+o$", "hello")
	return m
}

func Base64Enc() string { return base64.StdEncoding.EncodeToString([]byte("hi")) } // "aGk="

func Base64Dec() string {
	s, _ := base64.StdEncoding.DecodeString("aGk=")
	return string(s) // "hi"
}

func HexEnc() string { return hex.EncodeToString([]byte{104, 105}) } // "6869"

func UrlEsc() string { return url.QueryEscape("a b&c") } // "a+b%26c"

func UrlJoin() string {
	u, _ := url.JoinPath("https://x.example/base", "a", "b.txt")
	return u // "https://x.example/base/a/b.txt"
}

func HtmlEsc() string { return html.EscapeString("<b>&") } // "&lt;b&gt;&amp;"

func PathJoin() string { return path.Join("a", "b", "c.txt") } // "a/b/c.txt"

func PathSplit() string {
	d, f := path.Split("/a/b/c.txt")
	return d + "|" + f // "/a/b/|c.txt"
}

// JsonMarshal converts a script map to JSON through goJSON.
func JsonMarshal() string {
	b, err := json.Marshal(map[string]any{"x": 1, "ys": []string{"a", "b"}})
	if err != nil {
		return "err"
	}
	return string(b) // {"x":1,"ys":["a","b"]}
}

// JPoint feeds JsonMarshalStruct.
type JPoint struct {
	X int
	Y string
}

// JsonMarshalStruct converts a script struct to JSON through its field names.
func JsonMarshalStruct() string {
	b, err := json.Marshal(JPoint{X: 1, Y: "a"})
	if err != nil {
		return "err"
	}
	return string(b) // {"X":1,"Y":"a"}
}

// JsonUnmarshal decodes into the runtime value tree: map[string]any lands
// as a script map, numbers as float64.
func JsonUnmarshal() string {
	v, err := json.Unmarshal([]byte(`{"a": 1}`))
	if err != nil {
		return "err"
	}
	if v["a"] == 1.0 {
		return "ok"
	}
	return "bad"
}

// JsonUnmarshalTypeErr exercises the bound decoder's unmarshal-type
// errors: a kind mismatch reports json's UnmarshalTypeError text (with
// the root struct name and dotted field path) instead of storing a
// wrongly-typed value, and gc's partial-write rule keeps prior values at
// the failing node while successful siblings still land.
func JsonUnmarshalTypeErr() string {
	type BoolSchema struct{ Has *bool }
	type Schema struct {
		AP  BoolSchema `json:"ap"`
		Tag string     `json:"tag"`
	}
	type MBool bool

	var s Schema
	err := json.Unmarshal([]byte(`{"ap":true,"tag":"ok"}`), &s)
	out := fmt.Sprint(err, s.Tag, s.AP.Has)

	var m struct {
		B MBool `json:"b"`
		I int   `json:"i"`
	}
	m.B = MBool(true)
	err = json.Unmarshal([]byte(`{"b":"nope","i":1.5}`), &m)
	out += "|" + fmt.Sprint(err, bool(m.B), m.I)

	var deep struct {
		S []int `json:"s"`
	}
	err = json.Unmarshal([]byte(`{"s":[1,"x",3]}`), &deep)
	out += "|" + fmt.Sprint(err, deep.S)

	var i int
	err = json.Unmarshal([]byte(`"x"`), &i)
	out += "|" + fmt.Sprint(err)

	// gc reports the FIRST type error in input order — B's number
	// arrives before A's bad bool even though A decodes first by
	// field order.
	var es struct {
		A int
		B bool
	}
	err = json.Unmarshal([]byte(`{"B":123,"A":false}`), &es)
	out += "|" + fmt.Sprint(err)

	// json.Number keeps the literal — the number grammar is the only
	// check, so out-of-float64-range text stays and bad literals fail
	// without field context.
	var big json.Number
	err = json.Unmarshal([]byte(`"1e1000"`), &big)
	out += "|" + fmt.Sprint(big, err)
	var badn json.Number
	err = json.Unmarshal([]byte(`"abc"`), &badn)
	out += "|" + fmt.Sprint(err)
	return out
}

// JsonUnmarshalArray exercises fixed-array unmarshal: JSON positions
// decode into the element in place (a failed position keeps the prior
// value), positions past the JSON array reset to zero, extra elements
// are skipped without error, and null is a no-op — gc's [N]T rules.
func JsonUnmarshalArray() string {
	var a [2]int
	err := json.Unmarshal([]byte(`[1,2,3]`), &a)
	out := fmt.Sprint(err, a)

	b := [4]int{5, 6, 7, 8}
	err = json.Unmarshal([]byte(`[1]`), &b)
	out += "|" + fmt.Sprint(err, b)

	c := [2]int{5, 6}
	err = json.Unmarshal([]byte(`[1,"x","y"]`), &c)
	out += "|" + fmt.Sprint(err, c)

	err = json.Unmarshal([]byte(`null`), &c)
	out += "|" + fmt.Sprint(err, c)

	err = json.Unmarshal([]byte(`"str"`), &c)
	return out + "|" + fmt.Sprint(err, c)
}

// JsonUnmarshalStringOpt exercises the `,string` tag option: the field
// value must be a quoted literal of the field's kind — "42" for ints,
// "\"x\"" for strings, "true" for bools — parsed from the inner text
// (pointers apply it to the pointee, interfaces re-parse the inner as
// JSON, composites ignore it), and marshal emits the same quoted
// literal back.
func JsonUnmarshalStringOpt() string {
	type S struct {
		N int     `json:"n,string"`
		F float64 `json:"f,string"`
		B bool    `json:"b,string"`
		T string  `json:"t,string"`
		P *int    `json:"p,string"`
		A any     `json:"a,string"`
		L []int   `json:"l,string"`
	}
	var s S
	err := json.Unmarshal([]byte(`{"n":"42","f":"1.5","b":"true","t":"\"x\"","p":"3","a":"5","l":"[1]"}`), &s)
	out := fmt.Sprint(err, s.N, s.F, s.B, s.T, s.A, s.L)

	var s2 S
	err = json.Unmarshal([]byte(`{"n":"x"}`), &s2)
	out += "|" + fmt.Sprint(err, s2.N)
	err = json.Unmarshal([]byte(`{"n":42}`), &s2)
	out += "|" + fmt.Sprint(err, s2.N)
	err = json.Unmarshal([]byte(`{"b":"no"}`), &s2)
	out += "|" + fmt.Sprint(err, s2.B)
	err = json.Unmarshal([]byte(`{"t":"4"}`), &s2)
	out += "|" + fmt.Sprint(err, s2.T)

	b, _ := json.Marshal(S{N: 42, F: 1.5, B: true, T: "x", A: 7, L: []int{1}})
	out += "|" + string(b)

	// Composite fields ignore the option entirely: a non-string
	// literal decodes normally, not as an error.
	var cs struct {
		L []int          `json:"l,string"`
		M map[string]int `json:"m,string"`
	}
	err = json.Unmarshal([]byte(`{"l":[1],"m":{"k":2}}`), &cs)
	out += "|" + fmt.Sprint(err, cs.L, cs.M)

	// Named interface fields ignore the option both directions.
	type I interface{}
	var iv struct {
		V I `json:"v,string"`
	}
	err = json.Unmarshal([]byte(`{"v":7}`), &iv)
	out += "|" + fmt.Sprint(err, iv.V)
	ib, _ := json.Marshal(struct {
		V I `json:"v,string"`
	}{V: 7})
	out += "|" + string(ib)

	// A quoted "null" is the null literal: scalars keep their value
	// and pointer fields nil out.
	var nv struct {
		N int  `json:"n,string"`
		P *int `json:"p,string"`
	}
	seven := 7
	nv.N, nv.P = 7, &seven
	err = json.Unmarshal([]byte(`{"n":"null","p":"null"}`), &nv)
	return out + "|" + fmt.Sprint(err, nv.N, nv.P == nil)
}

// JsonUnmarshalMapKeys exercises non-string map keys: int/uint/float/
// named/pointer spellings parse from the key text (a bad key drops the
// pair and reports `number <key>` — or `string` for unsupported kinds
// like bool), and a bad element still inserts its zero value.
func JsonUnmarshalMapKeys() string {
	var m map[int]int
	err := json.Unmarshal([]byte(`{"3":1,"x":2,"5":3}`), &m)
	out := fmt.Sprint(err, m)
	err = json.Unmarshal([]byte(`{"3":"y","7":1}`), &m)
	out += "|" + fmt.Sprint(err, m)
	var u map[uint]int
	err = json.Unmarshal([]byte(`{"-1":1,"8":2}`), &u)
	out += "|" + fmt.Sprint(err, u)
	var f map[float64]int
	err = json.Unmarshal([]byte(`{"1.5":1,"x":2}`), &f)
	out += "|" + fmt.Sprint(err, f)
	var b map[bool]int
	err = json.Unmarshal([]byte(`{"true":1}`), &b)
	out += "|" + fmt.Sprint(err, b)
	type NK int
	var nk map[NK]int
	err = json.Unmarshal([]byte(`{"9":1}`), &nk)
	out += "|" + fmt.Sprint(err, len(nk))
	var sk map[string]int
	err = json.Unmarshal([]byte(`{"a":1}`), &sk)
	return out + "|" + fmt.Sprint(err, sk)
}

// JsonUnmarshalEmbed exercises anonymous-embed field flattening:
// promoted fields match their JSON keys through the embed chain
// (exact key before case-folded, shallowest depth first, tagged
// before untagged — a tie on all three drops the key silently, on
// marshal too), a `json:"name"` tag on the embed demotes it to a
// named member, and pointer embeds allocate on decode.
func JsonUnmarshalEmbed() string {
	type A struct{ X int }
	type B struct{ Y int }
	type Mid struct {
		A
		M int
	}
	type Outer struct {
		B
		Mid
		Z int
	}
	var o Outer
	err := json.Unmarshal([]byte(`{"x":1,"y":2,"m":3,"z":4}`), &o)
	out := fmt.Sprint(err, o.A.X, o.B.Y, o.Mid.M, o.Z)

	type A1 struct{ X int }
	type A2 struct{ X int }
	type Both struct {
		A1
		A2
	}
	var b Both
	err = json.Unmarshal([]byte(`{"x":1}`), &b)
	out += "|" + fmt.Sprint(err, b.A1.X, b.A2.X)
	m2, _ := json.Marshal(Both{A1: A1{X: 1}, A2: A2{X: 2}})
	out += "|" + string(m2)

	type Ptr struct {
		*A
		P int
	}
	var p Ptr
	err = json.Unmarshal([]byte(`{"x":7,"p":8}`), &p)
	out += "|" + fmt.Sprint(err, p.A != nil, p.A.X, p.P)

	type Tagd struct {
		A `json:"in"`
		B
	}
	var t Tagd
	err = json.Unmarshal([]byte(`{"in":{"x":1},"y":2}`), &t)
	out += "|" + fmt.Sprint(err, t.A.X, t.B.Y)
	m3, _ := json.Marshal(Tagd{A: A{X: 1}, B: B{Y: 2}})
	out += "|" + string(m3)

	m1, _ := json.Marshal(Outer{B: B{Y: 2}, Mid: Mid{A: A{X: 1}, M: 3}, Z: 4})
	out += "|" + string(m1)

	// A live pointer embed promotes its fields on marshal, and a
	// self-referential embed expands one level then stops.
	type Node struct {
		*Node
		X int
	}
	var nd Node
	err = json.Unmarshal([]byte(`{"x":7}`), &nd)
	out += "|" + fmt.Sprint(err, nd.X, nd.Node == nil)
	pb, _ := json.Marshal(Ptr{A: &A{X: 7}, P: 8})
	out += "|" + string(pb)
	nnb, _ := json.Marshal(Node{X: 9})
	out += "|" + string(nnb)

	// Folded-name lookup takes the FIRST live field in declaration
	// order — a same-name tie dies, but distinct names folding alike
	// do not.
	type Case struct {
		Foo int
		FOO int
	}
	var cf Case
	err = json.Unmarshal([]byte(`{"foo":7}`), &cf)
	return out + "|" + fmt.Sprint(err, cf.Foo, cf.FOO)
}

// StrconvAppendInt exercises the Append family — writeStatusLine in
// net/http formats the status code through it.
func StrconvAppendInt() string {
	return string(strconv.AppendInt(nil, 255, 16)) // "ff"
}

// bytesCut exercises bytes.Cut's (before, after, found) tuple.
func BytesCut() string {
	before, after, found := bytes.Cut([]byte("a:b"), []byte(":"))
	if !found {
		return "miss"
	}
	return string(before) + "|" + string(after)
}

// upperReader is a script-defined io.Reader: its Read method serves a
// canned string through the host callback proxy.
type upperReader struct {
	s   string
	pos int
}

func (r *upperReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.pos:])
	r.pos += n
	return n, nil
}

// IoReadAllScript feeds a script struct advertising Read to bound
// io.ReadAll — the proxy must forward and the byte buffer must flow
// back through the script slice.
func IoReadAllScript() string {
	b, err := io.ReadAll(&upperReader{s: "proxy-ok"})
	if err != nil {
		return "err"
	}
	return string(b)
}

// IoReadFullCopy verifies a host-side fill lands back in the script
// slice's elements.
func IoReadFullCopy() string {
	buf := make([]byte, 5)
	if _, err := io.ReadFull(strings.NewReader("hello world"), buf); err != nil {
		return "err"
	}
	return string(buf)
}

// scrSink is a script-defined io.Writer target for bufio.
type scrSink struct{ got string }

func (w *scrSink) Write(p []byte) (int, error) {
	w.got += string(p)
	return len(p), nil
}

// BufioOverScriptWriter writes through bound bufio.NewWriter —
// bufio.Reset must accept the script struct as an io.Writer (interface
// adaptation) and Flush must reach the script Write method.
func BufioOverScriptWriter() string {
	w := &scrSink{}
	bw := bufio.NewWriter(w)
	if _, err := bw.WriteString("buf"); err != nil {
		return "err"
	}
	if err := bw.Flush(); err != nil {
		return "flush"
	}
	return w.got
}

// AtomicInt64Ops runs the bound sync/atomic typed cell.
func AtomicInt64Ops() int64 {
	var n atomic.Int64
	n.Add(40)
	n.Store(2)
	return n.Load()
}

// ContextCancel confirms bound context constructors hand real host
// contexts back and their CancelFunc is deferred-callable.
func ContextCancel() bool {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	return ctx.Err() != nil
}

// AtomicAndOrOld: sync/atomic's And/Or return the value before the
// update, unlike Add which returns the new one.
func AtomicAndOrOld() int64 {
	var n int64 = 0b1100
	old := atomic.AndInt64(&n, 0b1010)
	return old*100 + n // 12*100 + 8 = 1208
}

// SlicesSortedSeq drives a bound seq producer (strings.SplitSeq returns
// a callable iter.Seq) — sorting must consume it through a yield call.
func SlicesSortedSeq() string {
	return strings.Join(slices.Sorted(strings.SplitSeq("b,a,c", ",")), "|")
}

// SlicesDeleteFuncAlias: DeleteFunc updates the shared backing — the
// original slice keeps its length, kept elements pack to the front, and
// the vacated tail is zeroed.
func SlicesDeleteFuncAlias() string {
	s := []int{1, 2, 3}
	_ = slices.DeleteFunc(s, func(i int) bool { return i == 2 })
	return fmt.Sprint(s[0], s[1], s[2]) // "1 3 0"
}

// IoMultiReaderEmpty: a zero-arg MultiReader is a valid empty reader.
func IoMultiReaderEmpty() int {
	b, _ := io.ReadAll(io.MultiReader())
	return len(b)
}

// IoCopyScriptPair: io.Copy probes src for WriterTo and dst for
// ReaderFrom — script structs declaring only Read/Write must still
// copy through those base methods.
func IoCopyScriptPair() string {
	src := &upperReader{s: "copy-pair"}
	dst := &scrSink{}
	if _, err := io.Copy(dst, src); err != nil {
		return "err"
	}
	return dst.got
}

// ContextAfterFunc registers then cancels inside the run: the callback
// may land before or after the process ends — either way it must not
// crash the host.
func ContextAfterFunc() string {
	ctx, cancel := context.WithCancel(context.Background())
	context.AfterFunc(ctx, func() {})
	cancel()
	return "ok"
}

// BufioReaderWriteToScript exercises the adapted-interface probe path:
// (*bufio.Reader).WriteTo asserts io.ReaderFrom on the dst writer — a
// writer that does not declare ReadFrom must copy through plain Write.
func BufioReaderWriteToScript() string {
	w := &scrSink{}
	br := bufio.NewReader(&upperReader{s: "via-writeto"})
	if _, err := br.WriteTo(w); err != nil {
		return "err"
	}
	return w.got
}

func main() {}
