package main

import "fmt"

func main() {
	defer func() {
		// recover() returns the panic's argument boxed in an interface —
		// a non-nil interface holding a nil *int reports non-nil like gc.
		r := recover()
		fmt.Println(r != nil)
	}()
	var p *int
	panic(p)
}
