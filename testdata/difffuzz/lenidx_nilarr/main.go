package main

import "fmt"

const N = 3

var s [][N]int
var m = map[string][N]int{}

func main() {
	fmt.Println(len(s[0]), cap(s[0]))
	fmt.Println(len(m["missing"]))
}
