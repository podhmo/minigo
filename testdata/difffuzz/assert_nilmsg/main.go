package main

import "fmt"

func main() {
	defer func() { fmt.Println(recover()) }()
	var x any
	_ = x.(int)
}
