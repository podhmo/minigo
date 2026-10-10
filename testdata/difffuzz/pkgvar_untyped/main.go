package main

import "fmt"

// a package-level `var x = 5` binds the literal default (int) like the
// in-function form, so x == d is a gc compile error.
var x = 5

type MyDur int64

var d MyDur = 1

func main() {
	fmt.Println(x == d)
}
