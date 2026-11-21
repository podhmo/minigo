package main

// Representative functions for the bytecode.Disasm goldens (M0-8).
// example.com/define only exists as a registered special form in the
// test; nothing here is run.
import (
	d "example.com/define"
)

func Add(a, b int) int { return a + b }

func Closure() func() int {
	x := 1
	return func() int {
		x++
		return x
	}
}

func Deferred() (r int) {
	defer func() { r = 9 }()
	r = 1
	return
}

func RangeSlice(xs []int) int {
	s := 0
	for _, v := range xs {
		s += v
	}
	return s
}

func RangeMap(m map[string]int) int {
	s := 0
	for k, v := range m {
		s += len(k) + v
	}
	return s
}

func MultiRet(x int) (int, int) { return x, x + 1 }

func NamedRet() (r int) {
	r = 3
	return
}

func Generic[T any](t T) T { return t }

func Goto() int {
	i := 0
loop:
	i++
	if i < 3 {
		goto loop
	}
	return i
}

func Variadic(xs ...int) int { return len(xs) }

func Spread(xs []int) int { return Variadic(xs...) }

func Special() {
	d.Convert(func(c *d.Config) {
		c.Map(1, 2)
	})
}

type Counter struct{ N int }

func (c *Counter) Bump() int {
	c.N++
	return c.N
}
