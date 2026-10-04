package main

import "fmt"

type T struct{ n int }

func (t *T) Hello() string {
	if t == nil {
		return "nil-T"
	}
	return fmt.Sprint(t.n)
}

func (t *T) Boom() int { return t.n }

func (t *T) Add(d int) { t.n += d }

type S struct{ *T }

func main() {
	var s S
	fmt.Println(s.Hello())

	func() {
		defer func() { fmt.Println("recovered:", recover()) }()
		s.Boom()
	}()

	s2 := S{T: &T{n: 5}}
	fmt.Println(s2.Hello())
	s2.Add(10)
	fmt.Println(s2.Hello(), s2.T.n)
}
