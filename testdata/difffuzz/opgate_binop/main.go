package main

import "fmt"

type Tag string
type Foo struct{ Tag Tag }

func main() {
	var f Foo
	var s string
	fmt.Println(f.Tag == s)
}
