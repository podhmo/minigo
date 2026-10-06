package main

import "fmt"

func main() {
	var e error
	var a any
	m := map[any]int{}
	m[any(e)] = 1
	m[a] += 2
	fmt.Println(len(m)*10 + m[any(e)])
}
