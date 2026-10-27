package main

import (
	"errors"
	"fmt"
	"strings"
)

type Ints []int

type Setter interface{ Set(xs ...int) }

type S struct{ v int }

func (s *S) Set(xs ...int) {
	for i := range xs {
		xs[i] += s.v
	}
}

func head(a int, xs ...int) {
	xs[0] = a
}

func fl(xs ...float64) { xs[0] *= 2 }

func sum[T int | float64](xs ...T) T {
	var t T
	for _, x := range xs {
		t += x
	}
	xs[0] = t
	return t
}

func rec(xs ...int) {
	if len(xs) == 0 {
		return
	}
	xs[0] = -xs[0]
	rec(xs[1:]...)
}

func grow(xs ...int) {
	xs = append(xs, 42)
	xs[0] = 9
}

type Name string

func nm(xs ...Name) { xs[0] = "z" }

func an(xs ...any) { xs[0] = "q" }

func zero[E any](xs ...E) {
	var z E
	xs[0] = z
}

type V struct{}

func (V) m(a int, xs ...int) { xs[0] = a }

type W struct{ V }

type Mer interface{ m(a int, xs ...int) }

func methodExprs() {
	xs := []int{1, 2}
	V.m(V{}, 3, xs...)
	fmt.Println(xs)
	(*V).m(&V{}, 4, xs...)
	fmt.Println(xs)
	W.m(W{}, 5, xs...)
	fmt.Println(xs)
	(*W).m(&W{}, 6, xs...)
	fmt.Println(xs)
	Mer.m(V{}, 7, xs...)
	fmt.Println(xs)
	func() {
		defer (*V).m(&V{}, 8, xs...)
	}()
	fmt.Println(xs)
}

func main() {
	methodExprs()
	c := func(ys ...int) { ys[1] = 3 }
	cs := []int{1, 2}
	c(cs...)
	zero(cs...)
	fmt.Println(cs)
	ns := []Name{"a"}
	nm(ns...)
	fmt.Println(ns)
	anys := []any{1, 2}
	an(anys...)
	fmt.Println(anys)

	xs := []int{1, 2, 3}
	head(7, xs...)
	fmt.Println(xs)
	var st Setter = &S{v: 10}
	st.Set(xs...)
	fmt.Println(xs)
	mv := (&S{v: 1}).Set
	mv(xs...)
	fmt.Println(xs)
	is := Ints{4, 5}
	head(0, is...)
	fmt.Println(is)
	fs := []float64{1.5, 2}
	fl(fs...)
	fmt.Println(fs)
	fmt.Println(sum(fs...), fs)
	fmt.Println(sum(xs...), xs)
	rec(xs...)
	fmt.Println(xs)
	g := make([]int, 1, 2)
	grow(g...)
	fmt.Println(g, g[:2])
	h := []int{1}
	grow(h...)
	fmt.Println(h)

	// builtins and host funcs still expand
	as := []any{"a", 1, true}
	fmt.Println(as...)
	fmt.Println(append(xs, xs...))
	ss := []string{"x", "y"}
	fmt.Println(strings.Join(append([]string{}, ss...), ","))
	errs := []error{errors.New("e1"), errors.New("e2")}
	fmt.Println(errors.Join(errs...))
	fmt.Println(fmt.Sprint(as...))

	// defer and go carry the slice too
	d := []int{1, 2}
	func() {
		defer head(5, d...)
		defer fmt.Println(as...)
	}()
	fmt.Println(d)
	done := make(chan bool)
	gs := []int{0}
	go func() {
		head(3, gs...)
		done <- true
	}()
	<-done
	fmt.Println(gs)
	var empty []int
	rec(empty...)
	rec([]int{}...)
	fmt.Println(sum([]int{4}...))
}
