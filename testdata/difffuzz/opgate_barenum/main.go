package main

import "fmt"

type Duration int64

func main() {
	var d Duration
	var x []int
	fmt.Println(d < len(x))
}
