package main

import "fmt"

type B bool

func main() {
	var v B = true
	var x int = 3
	w := v && x > 1
	var wb B
	fmt.Println(wb == (x < 5), w)
}
