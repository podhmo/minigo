package main

import "fmt"

type B bool

func (b B) String() string {
	if bool(b) {
		return "a"
	}
	return "z"
}

type S struct{ X B }

func main() {
	fmt.Println(map[[2]B]int{{true, false}: 1, {false, true}: 2})
	fmt.Println(map[S]int{{true}: 1, {false}: 2})
	fmt.Println(map[[2]int]int{{2, 1}: 1, {1, 2}: 2})
}
