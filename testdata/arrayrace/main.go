package main

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// Array-typed work shared between goroutines. Every function here is
// plain script code: the array types' AST (literal lengths, named const
// lengths, `[...]` literals) is shared by all goroutines running it, and
// the interpreter folds each evaluated length into the type on first use.

const N = 3
const M = N * 2

type pt struct{ X, Y int }

func (p pt) Sum() int { return p.X + p.Y }

type Summer interface{ Sum() int }

type Base struct{ ID int }

func (b Base) Name() string { return fmt.Sprint("base", b.ID) }

type Derived struct {
	Base
	Grid [N][N]int
	Pts  [2]pt
}

type Vec [N]int

func (v Vec) Total() int {
	t := 0
	for _, x := range v {
		t += x
	}
	return t
}

type Color int

const (
	Red Color = iota
	Green
	Blue
)

// String indexes a `[...]string` literal: its length folds on first use.
func (c Color) String() string { return [...]string{"R", "G", "B"}[c] }

type Stack[T any] struct{ items []T }

func (s *Stack[T]) Push(x T) { s.items = append(s.items, x) }
func (s *Stack[T]) Pop() T {
	x := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return x
}

type Pair[K comparable, V any] struct {
	K K
	V V
}

func Map[T, U any](xs []T, f func(T) U) []U {
	var out []U
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

func Zero[T any]() T {
	var z T
	return z
}

func Keys[K comparable, V any](m map[K]V) []K {
	var ks []K
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func mk() [2]pt { return [2]pt{{1, 1}, {2, 2}} }

func mkDerived() Derived {
	return Derived{Base: Base{ID: 1}, Pts: mk()}
}

func localConst() string {
	const k = 4
	var a [k]int
	var b [k * 2]string
	a[1] = 5
	a2 := a
	return fmt.Sprintf("%T %T %v %v", a, b, a == a2, len(b))
}

func ellipsis() string {
	a := [...]int{1, 2, 3}
	b := [...]string{2: "x"}
	c := [...]pt{{1, 2}, {3, 4}}
	return fmt.Sprintf("%T %T %T %v %v %v", a, b, c, len(a), len(b), c == [2]pt{{1, 2}, {3, 4}})
}

// work exercises array types through composite literals, copies, %T,
// len, range, comparison, struct fields, map keys, generic
// instantiation, interfaces, reflection and slicing.
func work() string {
	out := ""
	for i := 0; i < 10; i++ {
		a := mk()
		b := a
		b[0].X = 9
		out += fmt.Sprintf("%T %d %v %v|", a, len(a), a == b, b)
		for j, p := range a {
			out += fmt.Sprint(j, p.X)
		}
		d := mkDerived()
		d.Grid[1][2] = 7
		d2 := d
		d2.Grid[0][0] = 1
		out += fmt.Sprintf("%T %v %v %s|", d, d == d2, d2.Grid, d.Name())
		v := Vec{1, 2, 3}
		out += fmt.Sprint(v.Total(), len(v), v)
		var s Summer = d.Pts[1]
		out += fmt.Sprint(s.Sum())
		st := &Stack[[2]int]{}
		st.Push([2]int{1, 2})
		st.Push([2]int{3, 4})
		out += fmt.Sprint(st.Pop(), len(st.items))
		pr := Pair[[N]int, [2]pt]{K: [N]int{1, 2, 3}, V: mk()}
		out += fmt.Sprintf("%T %v", pr, pr)
		ms := map[[2]int]string{{1, 2}: "a", {3, 4}: "b"}
		ks := Keys(ms)
		sort.Slice(ks, func(i, j int) bool { return ks[i][0] < ks[j][0] })
		out += fmt.Sprint(ks, ms[[2]int{1, 2}])
		pm := map[[2]pt]int{}
		pm[a] = 1
		pm[mk()]++
		out += fmt.Sprint(pm[a], len(pm))
		zs := Zero[[M]int]()
		out += fmt.Sprintf("%T %v", zs, len(zs))
		us := Map([]int{1, 2}, func(x int) [2]int { return [2]int{x, x * 2} })
		out += fmt.Sprint(us)
		out += localConst()
		out += ellipsis()
		out += fmt.Sprint(Red, Blue)
		ch := make(chan [2]pt, 1)
		ch <- mk()
		out += fmt.Sprint(<-ch)
		rt := reflect.TypeOf(d.Grid)
		out += fmt.Sprint(rt.Kind(), rt.Len(), rt.Elem().Len(), rt.String())
		var ia interface{} = d.Pts
		switch x := ia.(type) {
		case [3]pt:
			out += "3"
		case [2]pt:
			out += fmt.Sprint("2", x[1].X)
		}
		var grid [N][2]*pt
		grid[1][1] = &pt{1, 2}
		cp := grid
		out += fmt.Sprint(cp[1][1].X, len(cp), len(cp[0]))
		sl := v[:]
		sl[0] = 100
		out += fmt.Sprint(sl, v)
	}
	return out
}

// Sequential is the reference: the same work on one goroutine.
func Sequential() string { return work() }

// Concurrent runs the same work on several goroutines released
// together, so they first touch every array type at the same time. It
// reports the common result, or the first goroutine that disagreed.
func Concurrent() string {
	const workers = 8
	var wg sync.WaitGroup
	res := make([]string, workers)
	gate := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			res[i] = work()
		}()
	}
	close(gate)
	wg.Wait()
	for i := 1; i < workers; i++ {
		if res[i] != res[0] {
			return fmt.Sprint("MISMATCH ", i)
		}
	}
	return res[0]
}

// Identity: a named const length, a function-local const length, a
// computed length and a non-decimal literal all fold to the same type as
// the plain literal `[3]int`.
func Identity() string {
	const k = 3
	var a [N]int
	var b [3]int
	var c [k]int
	var d [0x3]int
	var e [N + 0]int
	var ia, ib, ic, id, ie interface{} = a, b, c, d, e
	return fmt.Sprint(ia == ib, ib == ic, ic == id, id == ie, " ",
		fmt.Sprintf("%T %T %T %T %T", a, b, c, d, e))
}
