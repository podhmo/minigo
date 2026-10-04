package main

// ---- spread calls ----

func sum(nums ...int) int {
	t := 0
	for _, n := range nums {
		t += n
	}
	return t
}

func Spread() int {
	xs := []int{1, 2, 3}
	return sum(xs...) + sum(10, 20) // 6 + 30
}

func SpreadTail() int {
	return sum(5, []int{1, 2}...) // 8
}

// ---- compound assign & ++/-- on non-identifiers ----

type C struct{ N int }

func CompoundOps() int {
	s := &C{N: 1}
	s.N += 10 // 11
	s.N++     // 12
	xs := []int{1, 2}
	xs[0] += 5 // 6
	xs[1]++    // 3
	m := map[string]int{"k": 1}
	m["k"] *= 4                         // 4
	p := &s.N                           // FieldRef
	*p += 100                           // s.N = 112
	q := &xs[1]                         // IndexRef
	*q = 50                             // xs = [6,50]
	return s.N + xs[0] + xs[1] + m["k"] // 112+6+50+4
}

func PtrOps() int {
	v := 1
	p := &v
	*p += 10 // 11
	(*p)++   // 12
	return *p
}

// ---- labels, break/continue L, goto ----

func LabeledBreak() int {
	n := 0
Outer:
	for i := 0; i < 10; i++ {
		for j := 0; j < 10; j++ {
			if j == 3 {
				break Outer
			}
			n++
		}
		n += 100
	}
	return n // 3
}

func LabeledContinue() int {
	n := 0
Outer:
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if j > i {
				continue Outer
			}
			n++
		}
		n += 10 // reached only when the inner loop completes (i == 2)
	}
	return n // (1+2+3) + 10
}

func GotoSkip() int {
	n := 0
	goto skip
	n = 100
skip:
	n += 5
	goto end
	n += 1000
end:
	return n // 5
}

func LabeledSwitch() int {
	n := 0
Loop:
	for i := 0; i < 5; i++ {
		switch i {
		case 3:
			break Loop // exits the for, not the switch
		default:
			n++
		}
	}
	return n // 3
}

// ---- interfaces ----

type Shape interface {
	Area() int
}

type Sq struct{ Side int }

func (s Sq) Area() int { return s.Side * s.Side }

type Named interface{ Name() string }
type Tag struct{ Label string }

func (t Tag) Name() string { return t.Label }

type Wrapped struct{ Sq } // embedded: promoted Area()

func InterfaceDispatch() int {
	var sh Shape = Sq{Side: 4}
	return sh.Area() // 16
}

func EmbeddedMethod() int {
	w := Wrapped{Sq: Sq{Side: 5}}
	return w.Area() // 25
}

func IfaceHolds() int {
	var x any = "hello"
	if _, ok := x.(Shape); ok {
		return -1
	}
	var s Shape = Sq{Side: 6}
	if _, ok := s.(Shape); !ok { // satisfies itself
		return -2
	}
	if _, ok := s.(Named); ok { // Sq lacks Name()
		return -3
	}
	return 1
}

// ---- type assertions + switches ----

func AssertOK() (int, bool) {
	var x any = "hi"
	if v, ok := x.(string); ok {
		return len(v), ok // 2,true
	}
	return 0, false
}

func AssertFail() bool {
	var x any = 7
	_, ok := x.(string)
	return ok // false
}

func TypeSwitch() int {
	classify := func(v any) int {
		switch v.(type) {
		case int:
			return 1
		case string:
			return 2
		case bool:
			return 3
		default:
			return 0
		}
	}
	return classify(1)*100 + classify("x")*10 + classify(nil) // 120
}

func TypeSwitchBind() string {
	var x any = "hey"
	switch v := x.(type) {
	case int:
		return "int"
	case string:
		return v + "!" // narrowed binding
	default:
		return "?"
	}
}

func NilAssert() int {
	var x any // nil interface: no dynamic type
	if _, ok := x.(any); ok {
		return -1
	}
	if _, ok := x.(int); ok {
		return -2
	}
	switch x.(type) {
	case nil:
		return 1
	default:
		return -3
	}
}

func AssertPanic() int {
	n := 0
	func() {
		defer func() {
			if r := recover(); r != nil {
				n = 99
			}
		}()
		var x any = 1
		_ = x.(string) // panics: 1 is not string
	}()
	return n // 99
}

// ---- nil-slice semantics ----

func NilRange() int {
	var xs []int
	n := 0
	for range xs {
		n++ // zero iterations
	}
	for _, x := range xs {
		n += x
	}
	return n + sum(xs...) + sum() + len(xs) // 0
}

func AppendNil() int {
	var xs []int
	xs = append(xs, 1, 2)
	return len(xs) + xs[1] // 4
}

// ---- comma-ok zero values ----

func CommaOkZero() int {
	var x any = "s"
	v, ok := x.(int)
	if ok {
		return -1
	}
	return v + 5 // v binds the zero value -> 5
}

// ---- elided composite literal element types ----

type P2 struct{ X, Y int }
type Matrix [][]int

func ElidedLits() int {
	xs := [][]int{{1, 2}, {3, 4}}
	m := map[string][]int{"a": {9}}
	ps := []P2{{X: 1, Y: 2}, {3, 4}}
	return xs[0][0] + xs[1][1] + m["a"][0] + ps[0].X + ps[1].Y // 1+4+9+1+4
}

func NamedElided() int {
	m := Matrix{{1, 2}, {3}}
	return m[0][1] + m[1][0] // 2+3
}

// ---- named types over composite literals ----
// `type B A` where A is an array/slice/map keeps the declared tag but
// builds the underlying composite — the literal must not produce a
// bogus *Struct (ptr-element elision exercises the same path through
// []*B{{...}}).

type ArrA [2]int
type ArrB ArrA
type SlcA []int
type SlcB SlcA
type MapA map[string]int
type MapB MapA

func NamedCompArr() int {
	var b ArrB = ArrB{1, 2}
	return b[0] + b[1] // 3
}

func NamedCompSlice() int {
	var b SlcB = SlcB{3, 4}
	return b[1] // 4
}

func NamedCompMap() int {
	var b MapB = MapB{"x": 5}
	return b["x"] // 5
}

func NamedCompPtrElided() int {
	s := []*ArrB{{1, 2}}
	return (*s[0])[0] + (*s[0])[1] // 3
}

// ---- generics ----

func Id[T any](v T) T { return v }

func Fst[T, U any](a T, b U) T { return a }

func Convert[T any](x int) T { return T(x) } // binds T -> typedef in conversion

func GenericFns() int {
	return Id[int](40) + Fst[int, string](2, "x") // 42
}

func GenericConvert() int {
	return Convert[int](40) + 2 // 42
}

type Pair[T any] struct{ A, B T }

func (p Pair[T]) Sum() T { return p.A + p.B }

func GenericType() int {
	p := Pair[int]{A: 20, B: 22}
	return p.Sum() // 42
}

// ---- select with labels (regression: selects keep breaking right) ----

func SelectLabel() int {
	c := make(chan int)
	n := 0
Loop:
	for i := 0; i < 3; i++ {
		select {
		case v := <-c:
			n += v
		default:
			if i == 2 {
				break Loop
			}
			n++
		}
	}
	return n // i=0: n=1; i=1: n=2; i=2: break
}

// ---- range-over-func (iter.Seq/Seq2-style producers, Go 1.23) ----

func SeqIter() int {
	seq := func(yield func(int) bool) {
		if !yield(10) {
			return
		}
		if !yield(20) {
			return
		}
		if !yield(30) {
			return
		}
	}
	sum := 0
	for v := range seq {
		sum += v
	}
	return sum // 60
}

func SeqIter2() int {
	seq := func(yield func(int, string) bool) {
		if !yield(1, "a") {
			return
		}
		if !yield(2, "b") {
			return
		}
	}
	sum := 0
	for k, v := range seq {
		sum += k
		sum += len(v)
	}
	return sum // 1+1 + 2+1 = 5
}

func SeqBreak() int {
	seq := func(yield func(int) bool) {
		for i := 0; ; i++ {
			if !yield(i) {
				return
			}
		}
	}
	sum := 0
	for v := range seq {
		if v == 5 {
			break
		}
		sum += v
	}
	return sum // 0+1+2+3+4 = 10 — the infinite producer stops on yield==false
}

func SeqContinue() int {
	seq := func(yield func(int) bool) {
		if !yield(10) {
			return
		}
		if !yield(20) {
			return
		}
		if !yield(30) {
			return
		}
	}
	sum := 0
	for v := range seq {
		if v == 20 {
			continue
		}
		sum += v
	}
	return sum // 40
}

func SeqReturn() string {
	seq := func(yield func(int) bool) {
		if !yield(1) {
			return
		}
		if !yield(2) {
			return
		}
	}
	for v := range seq {
		if v == 2 {
			return "early exit"
		}
	}
	return "done"
}

func SeqGenerator() int {
	upTo := func(n int) func(func(int) bool) {
		return func(yield func(int) bool) {
			for i := 0; i < n; i++ {
				if !yield(i) {
					return
				}
			}
		}
	}
	sum := 0
	for v := range upTo(5) {
		sum += v
	}
	return sum // 0+1+2+3+4 = 10
}

func SeqNoVars() int {
	seq := func(yield func(int) bool) {
		yield(1)
		yield(2)
		yield(3)
	}
	n := 0
	for range seq {
		n++
	}
	return n // 3
}

func SeqNested() int {
	outer := func(yield func(int) bool) {
		if !yield(1) {
			return
		}
		if !yield(2) {
			return
		}
	}
	inner := func(yield func(int) bool) {
		if !yield(10) {
			return
		}
		if !yield(20) {
			return
		}
	}
	sum := 0
	for a := range outer {
		for b := range inner {
			sum += a * b
		}
	}
	return sum // 1*(10+20) + 2*(10+20) = 90
}

func SeqBodyPanic() (r int) {
	defer func() {
		if x := recover(); x != nil {
			r = 42
		}
	}()
	seq := func(yield func(int) bool) {
		if !yield(1) {
			return
		}
		if !yield(2) {
			return
		}
	}
	for v := range seq {
		if v == 2 {
			panic("stop")
		}
	}
	return -1
}

func SeqYieldAfterFalse() (r int) {
	defer func() {
		if x := recover(); x != nil {
			r = 7
		}
	}()
	seq := func(yield func(int) bool) {
		yield(1) // body breaks -> false
		yield(2) // must panic: iteration continued after yield returned false
	}
	for range seq {
		break
	}
	return 0
}

func SeqDefer() int {
	n := 0
	seq := func(yield func(int) bool) {
		defer func() { n += 9 }()
		if !yield(1) {
			return
		}
		yield(2)
	}
	for range seq {
		break
	}
	return n // 9 — producer's defer ran when it returned after yield==false
}

type seqFunc func(yield func(int) bool)

func SeqNamed() int {
	var seq seqFunc = func(yield func(int) bool) {
		yield(1)
		yield(2)
		yield(3)
	}
	sum := 0
	for v := range seq {
		sum += v
	}
	return sum // 6
}

func SeqLabelBreak() int {
	sum := 0
L:
	for {
		for v := range func(yield func(int) bool) {
			if !yield(1) {
				return
			}
			yield(2)
		} {
			sum += v
			break L
		}
		sum += 100
	}
	return sum // 1 — the labeled break exits through the func-iter loop
}

func main() {}

func SeqGotoLoop() int {
	sum := 0
	n := 0
Top:
	for v := range seqFunc(func(yield func(int) bool) {
		for i := 1; i <= 3; i++ {
			if !yield(i) {
				return
			}
		}
	}) {
		sum += v
		n++
		if n == 2 {
			goto Top
		}
	}
	return sum
}

// goto back to the for statement re-runs OpIter: a fresh iterator, same as
// Go re-entering the range clause (producer called again).
func SeqGotoLoop() int {
	sum := 0
	n := 0
Top:
	for v := range seqFunc(func(yield func(int) bool) {
		for i := 1; i <= 3; i++ {
			if !yield(i) {
				return
			}
		}
	}) {
		sum += v
		n++
		if n == 2 {
			goto Top
		}
	}
	return sum // 1+2 + 1+2+3 = 9
}
