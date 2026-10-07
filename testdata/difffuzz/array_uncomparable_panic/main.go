package main

import "fmt"

type bad [2][]int
type bad2 [2][3][]int
type wrap struct{ s []int }

func chk(f func() bool) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	msg = fmt.Sprint(f())
	return
}

func main() {
	// an uncomparable member shape panics on the enclosing type — Go
	// names the outermost uncomparable type.
	fmt.Println(chk(func() bool { return any(bad{}) == any(bad{}) }))
	fmt.Println(chk(func() bool { return any(bad2{}) == any(bad2{}) }))
	fmt.Println(chk(func() bool { return any(wrap{}) == any(wrap{}) }))
	fmt.Println(chk(func() bool { return any([2]wrap{}) == any([2]wrap{}) }))
	// interface-typed elements stay comparable: the panic names the
	// dynamic type instead.
	var i any = [2]any{[]int{}, []int{}}
	fmt.Println(chk(func() bool { return i == i }))
}
