package main

// Go 1.26 / 1.27 language deltas.
// Expected values verified against go1.27.1.

// ---- Go 1.26: new(expr) ----

type Box struct{ V int }

func NewTypeForm() int {
	p := new(int)
	return *p // 0
}

func NewExprVar() int {
	x := 5
	p := new(x)
	x = 99
	return *p // 5 — a copy, not an alias
}

func NewExprCall() int {
	p := new(1 + 41)
	return *p // 42
}

func NewExprStruct() int {
	p := new(Box{V: 6})
	p.V = 7
	return p.V // 7 — writes hit the allocated copy
}

func NewExprSelector() int {
	b := Box{V: 3}
	p := new(b.V)
	b.V = 10
	return *p // 3
}

func NewExprSlice() int {
	s := []int{1, 2}
	p := new(s) // *[]int: header copy shares the array, like Go
	q := *p
	return q[0] + s[1] // 3
}

func NewExprGeneric[T any](x T) T {
	p := new(x) // x is a value, typed by the argument's typedef
	return *p
}

func NewExprGenericCall() int {
	return NewExprGeneric(40) + NewExprGeneric(2) // 42
}

// ---- Go 1.26: self-referential type constraints ----

type Adder[A Adder[A]] interface {
	Add(A) A
}

type Int int

func (i Int) Add(o Int) Int { return i + o }

func addAll[A Adder[A]](xs []A) A {
	var acc A
	for _, x := range xs {
		acc = acc.Add(x)
	}
	return acc
}

func SelfRefCons() int {
	return int(addAll[Int]([]Int{1, 2, 4})) // 7
}

func SelfRefConsInfer() int {
	return int(addAll([]Int{2, 3})) // 5 — A inferred from the slice
}

type Plain int // no methods — cannot satisfy Adder[Plain]

func SelfRefConsBad() int {
	return int(addAll[Plain]([]Plain{1}))
}

// ---- Go 1.27: generic methods ----

type List[E any] []E

func (l List[E]) Reduce[R any](init R, f func(R, E) R) R {
	acc := init
	for _, e := range l {
		acc = f(acc, e)
	}
	return acc
}

func GenMethodInfer() int {
	l := List[int]{1, 2, 3}
	return l.Reduce(0, func(a, b int) int { return a + b }) // 6
}

func GenMethodExplicit() int {
	l := List[int]{1, 2}
	return l.Reduce[int](5, func(a, b int) int { return a * b }) // 10
}

func GenMethodExpr() int {
	l := List[int]{1, 2}
	// method expression — the receiver is arg 0, R infers from init
	return List[int].Reduce(l, 3, func(a, b int) int { return a + b*2 }) // 9
}

// a generic method on a non-generic type
func (b Box) Apply[T any](v T, f func(T) T) T { return f(v) }

func GenMethodConcreteRecv() int {
	b := Box{}
	return b.Apply(20, func(x int) int { return x + 1 }) + 1 // 22
}

// generic methods never satisfy an interface (Go 1.27 spec)
type Caller interface {
	Call(int) int
}

type Impl struct{}

func (Impl) Call[T any](v T) T { return v }

func GenMethodIfaceBad() int {
	var c Caller = Impl{} // generic Call is not in Impl's method set
	return c.Call(0)
}

// promoted generic method on an embedded generic type
type ListWrap struct {
	List[int]
	Y int
}

func GenMethodPromoted() int {
	w := ListWrap{List: List[int]{1, 2}}
	return w.Reduce(0, func(a, b int) int { return a + b*2 }) // 6
}

// ---- Go 1.27: promoted-field composite literal keys ----

type E1 struct{ V int }
type Mid struct{ E1 }
type Wrap struct {
	E1
	Y int
}
type Deep struct{ Mid }
type Shade struct {
	E1
	V int // shadows the promoted E1.V
}
type GBox[T any] struct{ F T }
type GWrap struct {
	GBox[int]
	Y int
}

func PromotedLitKey() int {
	w := Wrap{V: 9, Y: 2}
	return w.E1.V + w.Y // 11
}

func PromotedLitNested() int {
	d := Deep{V: 7}
	return d.Mid.E1.V // 7
}

func PromotedLitShadow() int {
	s := Shade{V: 3}
	return s.V + s.E1.V // 3 — the own field wins
}

func PromotedLitGeneric() int {
	g := GWrap{F: 5, Y: 1}
	return g.F + g.Y // 6
}

// promoted field read + write through the same machinery
func PromotedReadWrite() int {
	w := Wrap{Y: 1}
	w.V = 8
	return w.V + w.Y // 9
}

type A1 struct{ X int }
type A2 struct{ X int }
type Ambi struct {
	A1
	A2
}

func AmbigLitBad() int {
	a := Ambi{X: 1} // ambiguous: X lives on both embeds
	return a.A1.X
}

type PE struct{ *E1 }

func PromotedPtrPanic() int {
	p := PE{}
	return p.V // nil embedded pointer dereference panics like Go
}

// ---- Go 1.27: generalized function-type inference ----

func Id[T any](v T) T { return v }

func InferAssign() int {
	var h func(int) int = Id // T inferred from the declared signature
	return h(41) + 1         // 42
}

func InferComposite() int {
	fs := []func(int) int{Id, func(x int) int { return x * 2 }}
	return fs[0](40) + fs[1](1) // 42
}

func InferSend() int {
	ch := make(chan func(int) int, 1)
	ch <- Id
	f := <-ch
	return f(42) // 42
}

type Handler func(int) int

func InferConvert() int {
	var h Handler = Handler(Id) // conversion infers against F's signature
	return h(42)                // 42
}

func InferArg() int {
	apply := func(f func(int) int, x int) int { return f(x) }
	return apply(Id, 42) // 42
}

func InferReturn() int {
	get := func() func(int) int { return Id }
	return get()(7) + 35 // 42
}

func InferMethodAssign() int {
	l := List[int]{3}
	var g func(func(int, int) int, int) int = func(f func(int, int) int, n int) int {
		return l.Reduce(n, f)
	}
	return g(func(a, b int) int { return a + b }, 10) // 13
}

// receiver renaming: `func (b Box2[U])` re-binds U to the instantiation
type Box2[T any] struct{ V T }

func (b Box2[U]) Get() U { return b.V }

func RecvRename() int {
	b := Box2[int]{V: 5}
	return b.Get() // 5
}
