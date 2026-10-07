package main

import "fmt"

// slicing an array of a declared struct type yields []V that binds a
// []V slot — x/text/cases' `sparseBlocks{values: sparseValues[:]}`.

type V struct{ x int }

var arr = [2]V{{1}, {2}}

type S struct{ vs []V }

func main() {
	var vs []V = arr[:]
	s := S{vs: arr[:]}
	loc := [3]V{{4}, {5}, {6}}
	var ws []V = loc[1:]
	fmt.Println(len(vs), s.vs[1].x, ws[0].x)
}
