package main

import "fmt"

type uval uint

func main() {
	var u uint = 3
	p := (*uval)(&u)
	fmt.Println(*p)
	var w uval = 5
	q := (*uint)(&w)
	fmt.Println(*q)
}
