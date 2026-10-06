package main

import "fmt"

// A value method promoted into an interface through a nil *T must panic
// with Go's canonical text. $GOROOT/test/fixedbugs/issue19040.go.

type T int

type I interface {
	F()
}

func (t T) F() {}

var (
	t *T
	i I = t
)

func main() {
	defer func() {
		fmt.Println(recover())
	}()
	i.F()
}
