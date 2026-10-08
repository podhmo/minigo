package main

import "fmt"

type (
	A [3]int
	S struct{ x int }
	I interface{ m(x int) int }
	L []int
	M map[string]int
	C chan int
)

func (s S) m(x int) int { return x }

var (
	a A = [...]int{1, 2, 3}
	s S = struct{ x int }{0}
	i I = s
	l L = []int{1, 2}
	m M = map[string]int{"foo": 0}
	c C = make(chan int)
)

// Anonymous literals converted into named slots must take the declared
// tag (method set + name), and an anonymous conversion result rebinds a
// named slot when the underlying types are identical (corpus bug277).
func main() {
	a = A(a)
	a = [3]int(a)
	s = struct{ x int }(s)
	i = (interface{ m(x int) int })(s)
	i = interface{ m(x int) int }(s)
	l = []int(l)
	m = map[string]int(m)
	c = chan int(c)
	_ = chan<- int(c)
	_ = (<-chan int)(c)
	fmt.Println(a, s, i.m(7), l, m, c == nil)
}
