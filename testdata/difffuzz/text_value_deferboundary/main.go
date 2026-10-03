// A panic raised inside a deferred call during a normal-return drain
// unwinds into the owner frame — recover() in a later deferred call is
// exactly one frame away and catches it (recover.go's try() shape).
package main

import "fmt"

func try(g func(), deflt int) (x int) {
	defer func() {
		if v := recover(); v != nil {
			x = v.(int)
		}
	}()
	defer g()
	return deflt
}

func main() {
	fmt.Println(try(func() { panic(5) }, 55))
	fmt.Println(try(func() {}, 77))
}
