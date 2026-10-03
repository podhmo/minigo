package main

import "fmt"

// try reports what recover() sees for each runtime failure — payload
// text and whether it is an error value, like Go's plainError /
// errorString / boundsError families.
func try(f func()) (out string) {
	defer func() {
		if r := recover(); r != nil {
			_, isErr := r.(error)
			out = fmt.Sprintf("%v [error=%v]", r, isErr)
		}
	}()
	f()
	return "no panic"
}

func main() {
	fmt.Println(try(func() { var m map[int]int; m[0] = 1 }))
	fmt.Println(try(func() { var s []int; _ = s[5] }))
	fmt.Println(try(func() { var p *int; _ = *p }))
	var a, b any = []int{1}, []int{1}
	fmt.Println(try(func() { _ = a == b }))
	i, n := 1, -1
	fmt.Println(try(func() { _ = i << n }))
	bad := -1
	fmt.Println(try(func() { _ = make([]int, bad) }))
	fmt.Println(try(func() { _ = make([]int, 0, bad) }))
	var c chan int
	fmt.Println(try(func() { close(c) }))
	fmt.Println(try(func() {
		for range func(y func(int) bool) { y(1); y(2) } {
			break
		}
	}))
}
