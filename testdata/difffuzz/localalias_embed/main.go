package main

import "fmt"

func main() {
	var x, y interface{}
	{
		type C = int32
		x = struct{ C }{}
	}
	{
		type C = uint32
		y = struct{ C }{}
	}
	fmt.Println(x == y)
	type C = int32
	fmt.Println(x == struct{ C }{}, x == struct{ int32 }{})
}
