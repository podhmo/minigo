package main

// Go 1.27-only syntax: go/parser on a go1.26 toolchain rejects method
// type parameters entirely, so this package lives in its own directory
// and the test skips itself when the host toolchain is older.

// ---- generic methods ----

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
type Point struct{ X, Y int }

func (p Point) Apply[T any](v T, f func(T) T) T { return f(v) }

func GenMethodConcreteRecv() int {
	p := Point{}
	return p.Apply(20, func(x int) int { return x + 1 }) + 1 // 22
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

// a generic method bound to a func-typed variable infers from it
func InferMethodAssign() int {
	l := List[int]{3}
	var g func(int, func(int, int) int) int = l.Reduce
	return g(10, func(a, b int) int { return a + b }) // 13
}
