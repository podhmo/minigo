package main

import "fmt"

var p *int

func set() {
	a := 7
	p = &a
}

func main() {
	set()
	fmt.Println(*p)
}
