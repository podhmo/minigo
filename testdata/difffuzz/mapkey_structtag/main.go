package main

import "fmt"

func main() {
	var a any = struct{ int }{}
	var b any = struct {
		int "x"
	}{}
	m := map[interface{}]int{}
	m[a] = 1
	m[b] = 2
	fmt.Println(len(m))
}
