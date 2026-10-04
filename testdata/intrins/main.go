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

func Utf8Encode() string { return utf8.EncodeRune('☺') } // "☺" — script-shaped: returns a string

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

func main() {}
