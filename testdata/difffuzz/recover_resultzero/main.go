package main

import "fmt"

func f() int {
	defer func() {
		recover()
	}()
	panic("oops")
}

func g() (int, string) {
	defer func() { recover() }()
	panic("oops")
}

func h() (x int) {
	defer func() { recover() }()
	panic("oops")
}

func main() {
	g() // leave a result on the stack, like bug254's original
	fmt.Println(f())
	x, s := g()
	fmt.Println(x, s)
	fmt.Println(h())
}
