package main

import "fmt"

var intp *int
var sp *[2]int

func try(f func()) (p bool) {
	defer func() { p = recover() != nil }()
	f()
	return
}

func main() {
	fmt.Println(try(func() { println(&*intp) }))
	fmt.Println(try(func() { println(&sp[0]) }))
	var s []int
	fmt.Println(try(func() { println(&s[0]) }))
}
