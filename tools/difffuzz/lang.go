package main

// The lang domain targets the language core a script leans on beyond
// string helpers: user-defined structs with value/pointer methods,
// embedding and promotion, interfaces (typed nils, assertions, type
// switches, fmt's Stringer/error dispatch), control flow (labels, goto,
// fallthrough, defer/recover, per-iteration loop variables, range-over-int
// and range-over-func, channels) and user-defined generics. It reuses the
// text domain's signature machinery; the prelude below is shared by every
// probe and uses only fmt, which every program imports.
//
// Package-level values are shared across probes and minigo re-runs from
// the probe after a trap with fresh globals, so templates never mutate
// them: mutation goes through a local copy.

var (
	lPoint = &Typ{Name: "Point", VarKey: "pt", Kind: "struct", Values: []string{"Point{}", "Point{1, 2}", "Point{-3, 4}"}}
	lPP    = &Typ{Name: "*Point", VarKey: "pp", Kind: "ptr", Values: []string{"nil", "&Point{5, 6}"}}
	lLab   = &Typ{Name: "Labeled", VarKey: "lab", Kind: "struct", Values: []string{"Labeled{}", `Labeled{Point{1, 2}, "a"}`}}
	lShape = &Typ{Name: "Shape", VarKey: "sh", Kind: "iface", Values: []string{"nil", "Rect{2, 3}", "&Tri{3, 4}", "(*Tri)(nil)"}}
	lErr   = &Typ{Name: "error", VarKey: "err", Kind: "iface", Values: []string{"nil", "&MyErr{404}", "(*MyErr)(nil)"}}
	lAny   = &Typ{Name: "any", VarKey: "any", Kind: "iface", Values: []string{"nil", "1", `"s"`, "Point{1, 2}", "&Point{3, 4}", "[]int{1}", "3.5", "Rect{1, 1}", "Celsius(2)"}}
	lIS    = &Typ{Name: "[]int", VarKey: "is", Kind: "slice", Values: []string{"nil", "[]int{}", "[]int{3, 1, 2}", "[]int{5, -1, 5, 0}"}}
	lInt   = &Typ{Name: "int", VarKey: "n", Kind: "int", Values: []string{"0", "1", "2", "3", "-1", "5"}}
	lStr   = &Typ{Name: "string", VarKey: "s", Kind: "string", Values: []string{`""`, `"a"`, `"héllo"`, `"x,y"`}}

	langTypes = []*Typ{lPoint, lPP, lLab, lShape, lErr, lAny, lIS, lInt, lStr, xBool, xSS, xMap, xFloat}
)

const langDecls = `type Point struct{ X, Y int }

func (p Point) String() string    { return fmt.Sprintf("(%d,%d)", p.X, p.Y) }
func (p Point) Add(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }
func (p *Point) Scale(k int)      { p.X *= k; p.Y *= k }

type Labeled struct {
	Point
	Label string
}

// Counter's String has a pointer receiver: fmt uses it for *Counter only.
type Counter struct{ N int }

func (c *Counter) String() string { return fmt.Sprint("#", c.N) }

type Shape interface {
	Area() int
	Kind() string
}

type Rect struct{ W, H int }

func (r Rect) Area() int      { return r.W * r.H }
func (r Rect) Kind() string   { return "rect" }
func (r Rect) String() string { return fmt.Sprintf("%dx%d", r.W, r.H) }

type Tri struct{ B, H int }

func (t *Tri) Area() int    { return t.B * t.H / 2 }
func (t *Tri) Kind() string { return "tri" }

type MyErr struct{ Code int }

func (e *MyErr) Error() string { return fmt.Sprint("code ", e.Code) }

type Celsius float64

type Number interface{ ~int | ~float64 }

type Ordered interface{ ~int | ~float64 | ~string }

func Sum[T Number](xs ...T) T {
	var t T
	for _, x := range xs {
		t += x
	}
	return t
}

func MaxOf[T Ordered](a, b T) T {
	if a > b {
		return a
	}
	return b
}

func Map[T, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func Filter[T any](xs []T, keep func(T) bool) []T {
	var out []T
	for _, x := range xs {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

func Reduce[T, A any](xs []T, acc A, f func(A, T) A) A {
	for _, x := range xs {
		acc = f(acc, x)
	}
	return acc
}

func SortedKeys[K Ordered, V any](m map[K]V) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j] < ks[j-1]; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
	return ks
}

func Describe[T any](v T) string { return fmt.Sprintf("%T:%v", v, v) }

func KindOf[T any](v T) string {
	switch any(v).(type) {
	case nil:
		return "nil"
	case int:
		return "int"
	case string:
		return "string"
	case fmt.Stringer:
		return "stringer"
	case error:
		return "error"
	}
	return "other"
}

func Zero[T any]() T { var z T; return z }

func Ptr[T any](v T) *T { return &v }

func JoinS[T fmt.Stringer](xs []T) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += "+"
		}
		out += x.String()
	}
	return out
}

type Stack[T any] struct{ items []T }

func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }
func (s *Stack[T]) Len() int { return len(s.items) }
func (s *Stack[T]) Pop() (T, bool) {
	var z T
	if len(s.items) == 0 {
		return z, false
	}
	v := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return v, true
}

type Pair[K comparable, V any] struct {
	Key K
	Val V
}

func (p Pair[K, V]) String() string { return fmt.Sprintf("%v=%v", p.Key, p.Val) }`

var langSigs = []*Sig{
	// --- structs: value/pointer methods, method values/expressions, value semantics
	sig("PointLit", lPoint, "(Point{X: $1, Y: $2})", lInt, lInt),
	sig("Point.Add", lPoint, "$1.Add($2)", lPoint, lPoint),
	sig("Point.Scale", lPoint, "func() Point { c := $1; c.Scale($2); return c }()", lPoint, lInt),
	sig("methodValue", lPoint, "func() Point { f := $1.Add; return f($2) }()", lPoint, lPoint),
	sig("methodExpr", lPoint, "Point.Add($1, $2)", lPoint, lPoint),
	sig("ptrMethodExpr", lPoint, "func() Point { c := $1; (*Point).Scale(&c, $2); return c }()", lPoint, lInt),
	sig("boundBeforeMut", lPoint, "func() Point { c := $1; f := c.Add; c.X = 100; return f($2) }()", lPoint, lPoint),
	sig("Point.String", lStr, "$1.String()", lPoint),
	sig("Point%v", lStr, `fmt.Sprintf("%v|%+v|%s|%d", $1, $1, $1, $1)`, lPoint),
	sig("Point%#v", lStr, `fmt.Sprintf("%#v", $1)`, lPoint),
	sig("Point.X", lInt, "$1.X", lPoint),
	sig("Point==", xBool, "($1 == $2)", lPoint, lPoint),
	sig("structMapKey", lInt, "func() int { m := map[Point]int{}; m[$1]++; m[$2]++; return len(m)*10 + m[$1] }()", lPoint, lPoint),
	sig("arrayCopy", lInt, "func() int { a := [2]Point{$1, $2}; b := a; b[0].X = 99; return a[0].X }()", lPoint, lPoint),
	sig("sliceAlias", lInt, "func() int { s := []Point{$1}; t := s; t[0].X = 7; return s[0].X }()", lPoint),
	sig("rangeCopy", lInt, "func() int { s := []Point{$1, $2}; for _, p := range s { p.X = 100 }; return s[0].X + s[1].X }()", lPoint, lPoint),
	sig("rangeIndexMut", lInt, "func() int { s := []Point{$1, $2}; for i := range s { s[i].Scale(2) }; return s[0].X + s[1].Y }()", lPoint, lPoint),
	sig("mapStructCopy", lInt, `func() int { m := map[string]Point{"k": $1}; p := m["k"]; p.X = 50; return m["k"].X }()`, lPoint),
	sig("anonStruct", lStr, `fmt.Sprint(struct{ A int; B string }{$1, $2})`, lInt, lStr),
	sig("Counter%v", lStr, `fmt.Sprint(Counter{$1}, " ", &Counter{$1})`, lInt),

	// --- embedding and promotion
	sig("LabeledLit", lLab, "(Labeled{Point: $1, Label: $2})", lPoint, lStr),
	sig("Labeled%v", lStr, `fmt.Sprintf("%v|%+v", $1, $1)`, lLab),
	sig("Labeled.X", lInt, "$1.X", lLab),
	sig("Labeled.Point", lPoint, "$1.Point", lLab),
	sig("Labeled.Add", lPoint, "$1.Add($2)", lLab, lPoint),
	sig("Labeled.Scale", lLab, "func() Labeled { c := $1; c.Scale($2); return c }()", lLab, lInt),
	sig("Labeled==", xBool, "($1 == $2)", lLab, lLab),

	// --- pointers
	sig("addrOf", lPP, "(&Point{$1, $2})", lInt, lInt),
	sig("newPoint", lPP, "func() *Point { p := new(Point); p.X = $1; return p }()", lInt),
	sig("Ptr", lPP, "Ptr($1)", lPoint),
	sig("deref", lPoint, "(*$1)", lPP),
	sig("ptr.X", lInt, "$1.X", lPP),
	sig("ptr.Add", lPoint, "$1.Add($2)", lPP, lPoint),
	sig("*Point%v", lStr, `fmt.Sprintf("%v|%+v", $1, $1)`, lPP),
	sig("ptr==nil", xBool, "($1 == nil)", lPP),
	sig("ptrEq", xBool, "($1 == $2)", lPP, lPP),
	sig("ptrAlias", lInt, "func() int { c := *$1; p := &c; q := p; q.X = 9; return p.X + c.X }()", lPP),

	// --- interfaces: dispatch, typed nil, assertions, type switches
	sig("Shape.Area", lInt, "$1.Area()", lShape),
	sig("Shape.Kind", lStr, "$1.Kind()", lShape),
	sig("Shape==nil", xBool, "($1 == nil)", lShape),
	sig("Shape==", xBool, "($1 == $2)", lShape, lShape),
	sig("Shape%v", lStr, `fmt.Sprintf("%v|%T", $1, $1)`, lShape),
	sig("rectShape", lShape, "Shape(Rect{$1, $2})", lInt, lInt),
	sig("triShape", lShape, "func() Shape { if $1 > 1 { return &Tri{$1, $2} }; var t *Tri; return t }()", lInt, lInt),
	sig("assertRect", lStr, "fmt.Sprint($1.(Rect))", lShape),
	sig("assertOk", xBool, "func() bool { _, ok := $1.(Rect); return ok }()", lShape),
	sig("asStringer", lStr, `func() string { if s, ok := $1.(fmt.Stringer); ok { return s.String() }; return "-" }()`, lShape),
	sig("typeSwitchShape", lStr, `func() string { switch s := $1.(type) { case Rect: return fmt.Sprint("R", s.W); case *Tri: if s == nil { return "nilTri" }; return fmt.Sprint("T", s.B); case nil: return "nil"; default: return "?" } }()`, lShape),
	sig("totalArea", lInt, "func() int { t := 0; for _, s := range []Shape{$1, $2} { if s != nil { t += s.Area() } }; return t }()", lShape, lShape),
	sig("boxPoint", lAny, "any($1)", lPoint),
	sig("boxPtr", lAny, "any($1)", lPP),
	sig("boxShape", lAny, "any($1)", lShape),
	sig("boxInt", lAny, "any($1)", lInt),
	sig("boxIS", lAny, "any($1)", lIS),
	sig("anyTypeSwitch", lStr, `func() string { switch v := $1.(type) { case nil: return "nil"; case int: return fmt.Sprint("int", v+1); case string: return "str" + v; case fmt.Stringer: return "S:" + v.String(); case []int: return fmt.Sprint("len", len(v)); case error: return "E"; default: return fmt.Sprintf("%T", v) } }()`, lAny),
	sig("anyMultiCase", lStr, `func() string { switch v := $1.(type) { case int, float64: return fmt.Sprintf("num:%v:%T", v, v); case Point, *Point: return fmt.Sprintf("pt:%v", v) }; return "-" }()`, lAny),
	sig("any==", xBool, "($1 == $2)", lAny, lAny),
	sig("anyMapKey", lInt, "func() int { m := map[any]int{}; m[$1] = 1; m[$2] += 2; return len(m)*10 + m[$1] }()", lAny, lAny),
	sig("assertInt", lInt, "$1.(int)", lAny),
	sig("Describe", lStr, "Describe($1)", lAny),
	sig("DescribeShape", lStr, "Describe($1)", lShape),
	sig("KindOf", lStr, "KindOf($1)", lAny),
	sig("KindOfPoint", lStr, "KindOf($1)", lPoint),

	// --- error values
	sig("err.Error", lStr, "$1.Error()", lErr),
	sig("err==nil", xBool, "($1 == nil)", lErr),
	sig("err%v", lStr, `fmt.Sprintf("%v|%T", $1, $1)`, lErr),
	sig("errCode", lInt, "func() int { if e, ok := $1.(*MyErr); ok && e != nil { return e.Code }; return -1 }()", lErr),
	sig("mkErr", lErr, "func() error { if $1 > 2 { return &MyErr{$1} }; return nil }()", lInt),
	sig("typedNilErr", lErr, "func() error { var p *MyErr; if $1 { return p }; return nil }()", xBool),
	sig("errAsAny", lAny, "any($1)", lErr),

	// --- control flow
	sig("labeledLoop", lInt, "func() int { n := 0; outer: for i := 0; i < $1; i++ { for j := 0; j < $2; j++ { if j > i { continue outer }; if i*j > 6 { break outer }; n++ } }; return n }()", lInt, lInt),
	sig("goto", lInt, "func() int { i, n := 0, 0; loop: if i < $1 { n += i; i++; goto loop }; return n }()", lInt),
	sig("fallthrough", lStr, `func() string { out := ""; switch { case $1 > 2: out += "a"; fallthrough; case $1 > 0: out += "b"; default: out += "c" }; return out }()`, lInt),
	sig("switchInit", lStr, `func() string { switch x := $1 % 3; x { case 0: return "zero"; case 1, -1: return "one"; default: return fmt.Sprint(x) } }()`, lInt),
	sig("labeledSwitchBreak", lInt, "func() int { n := 0; L: for i := range 5 { switch { case i == $1: break L; case i%2 == 0: continue }; n += i }; return n }()", lInt),
	sig("deferLoopVar", lStr, `func() (s string) { for i := 0; i < $1 && i < 5; i++ { defer func() { s += fmt.Sprint(i) }() }; return "r" }()`, lInt),
	sig("deferArgEval", lStr, "func() (s string) { x := $1; defer func(v int) { s += fmt.Sprint(v, x) }(x); x += 10; return \"r\" }()", lInt),
	sig("deferResult", lInt, "func() (n int) { defer func() { n *= 2 }(); return $1 + 1 }()", lInt),
	sig("deferMethodValue", lStr, "func() (s string) { p := $1; defer func(f func() string) { s = f() }(p.String); p.X = 77; return \"\" }()", lPoint),
	sig("recoverValue", lStr, `func() (s string) { defer func() { if r := recover(); r != nil { s = fmt.Sprint("rec:", r) } }(); if $1 > 2 { panic($2) }; return "ok" }()`, lInt, lStr),
	sig("recoverError", lStr, `func() (s string) { defer func() { if e, ok := recover().(error); ok { s = "err:" + e.Error() } }(); a := []int{1}; return fmt.Sprint(a[$1]) }()`, lInt),
	sig("recoverCustom", lInt, "func() (n int) { defer func() { if e, ok := recover().(*MyErr); ok { n = e.Code } }(); panic(&MyErr{$1}) }()", lInt),
	sig("rePanic", lStr, `func() (s string) { defer func() { s = fmt.Sprint(recover()) }(); defer func() { panic("second") }(); panic($1) }()`, lStr),
	sig("closureLoopVar", lStr, "func() string { var fs []func() int; for i := range $1 { fs = append(fs, func() int { return i }) }; out := \"\"; for _, f := range fs { out += fmt.Sprint(f()) }; return out }()", lInt),
	sig("closureCounter", lInt, "func() int { c := 0; inc := func() int { c++; return c }; inc(); inc(); return c + inc()*$1 }()", lInt),
	sig("closureShared", lStr, "func() string { x := $1; get := func() int { return x }; set := func(v int) { x = v }; set(x + 5); return fmt.Sprint(get(), x) }()", lInt),
	sig("rangeFunc", lStr, `func() string { seq := func(yield func(int) bool) { for i := 0; i < 10; i++ { if !yield(i * $1) { return } } }; out := ""; for v := range seq { if v > 6 { break }; out += fmt.Sprint(v, ",") }; return out }()`, lInt),
	sig("rangeFunc2", lStr, `func() string { seq := func(yield func(int, string) bool) { for i, x := range $1 { if !yield(i, x) { return } } }; out := ""; for i, x := range seq { out += fmt.Sprint(i, x, ";") }; return out }()`, xSS),
	sig("rangeFuncDefer", lStr, `func() (s string) { seq := func(yield func(int) bool) { for i := range 3 { if !yield(i) { return } } }; for v := range seq { defer func() { s += fmt.Sprint(v) }(); if v == $1 { break } }; return "r" }()`, lInt),
	sig("multiReturn", lStr, "func() string { f := func(a, b int) (int, int) { return b, a }; x, y := f($1, $2); return fmt.Sprint(x, y) }()", lInt, lInt),
	sig("variadic", lInt, "func() int { f := func(xs ...int) int { return len(xs) }; return f($1...) + f() + f($2, $2) }()", lIS, lInt),
	sig("selectDefault", lStr, `func() string { ch := make(chan int, 1); if $1 > 0 { ch <- $1 }; select { case v := <-ch: return fmt.Sprint("got", v); default: return "none" } }()`, lInt),
	sig("chanRange", lInt, "func() int { ch := make(chan int, 8); for i := range 5 { ch <- i * $1 }; close(ch); t := 0; for v := range ch { t += v }; return t }()", lInt),
	sig("chanClosedRecv", lStr, "func() string { ch := make(chan int, 1); ch <- $1; close(ch); a, ok1 := <-ch; b, ok2 := <-ch; return fmt.Sprint(a, ok1, b, ok2) }()", lInt),
	sig("closeTwice", lInt, "func() int { ch := make(chan int); close(ch); if $1 > 0 { close(ch) }; return 0 }()", lInt),
	sig("goroutineWG", lInt, "func() int { var wg sync.WaitGroup; var mu sync.Mutex; t := 0; for i := range 4 { wg.Add(1); go func() { defer wg.Done(); mu.Lock(); t += i * $1; mu.Unlock() }() }; wg.Wait(); return t }()", lInt),
	sig("goroutinePipe", lStr, "func() string { in := make(chan int); out := make(chan string); go func() { for v := range in { out <- fmt.Sprint(v * 2) }; close(out) }(); go func() { for _, x := range $1 { in <- x }; close(in) }(); s := \"\"; for v := range out { s += v + \",\" }; return s }()", lIS),

	// --- generics
	sig("Map", xSS, "Map($1, func(x int) string { return fmt.Sprint(x * 2) })", lIS),
	sig("MapPoint", lStr, "fmt.Sprint(Map($1, func(x int) Point { return Point{x, -x} }))", lIS),
	sig("Filter", lIS, "Filter($1, func(x int) bool { return x > $2 })", lIS, lInt),
	sig("Reduce", lInt, "Reduce($1, $2, func(a, x int) int { return a*2 + x })", lIS, lInt),
	sig("ReduceStr", lStr, `Reduce($1, "", func(a string, x string) string { return a + "|" + x })`, xSS),
	sig("Sum", lInt, "Sum($1...)", lIS),
	sig("SumFloat", xFloat, "Sum[float64](1.5, $1)", xFloat),
	sig("SumCelsius", lAny, "any(Sum(Celsius($1), 2))", xFloat),
	sig("MaxOf", lStr, "MaxOf($1, $2)", lStr, lStr),
	sig("MaxOfInt", lInt, "MaxOf($1, $2)", lInt, lInt),
	sig("SortedKeys", xSS, "SortedKeys($1)", xMap),
	sig("Stack", lStr, "func() string { var s Stack[string]; for _, x := range $1 { s.Push(x) }; v, ok := s.Pop(); return fmt.Sprint(v, ok, s.Len()) }()", xSS),
	sig("StackPoint", lStr, "func() string { s := &Stack[Point]{}; s.Push($1); s.Push($2); v, _ := s.Pop(); w, _ := s.Pop(); _, ok := s.Pop(); return fmt.Sprint(v, w, ok) }()", lPoint, lPoint),
	sig("Pair", lAny, "any(Pair[string, int]{$1, $2})", lStr, lInt),
	sig("Pair%T", lStr, `fmt.Sprintf("%T|%v|%+v", Pair[Point, []int]{$1, $2}, Pair[Point, []int]{$1, $2}, Pair[int, bool]{})`, lPoint, lIS),
	sig("Zero", lStr, `fmt.Sprintf("%v|%v|%q|%v", Zero[Point]() == $1, Zero[*Point]() == nil, Zero[string](), Zero[Shape]())`, lPoint),
	sig("JoinS", lStr, "JoinS([]Point{$1, $2})", lPoint, lPoint),
	sig("JoinSRect", lStr, "JoinS([]Rect{{$1, $2}, {$2, $1}})", lInt, lInt),
	sig("genericClosure", lInt, "func() int { add := func(n int) func(int) int { return func(x int) int { return x + n } }; return Reduce(Map($1, add($2)), 0, func(a, x int) int { return a + x }) }()", lIS, lInt),
}

var langImports = []string{"fmt", "sync"}

var langDomain = &Domain{Name: "lang", Types: langTypes, Imports: langImports, Decls: []string{langDecls}, Sigs: langSigs}
