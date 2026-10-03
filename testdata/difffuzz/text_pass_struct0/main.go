// run
package main

import "fmt"

// anonymous struct types assert by shape: two spellings of the same
// field list are the same type, different field names/types are not.

func main() {
	var e interface{} = struct{}{}
	_, ok := e.(struct{})
	fmt.Println(ok)

	var s interface{} = struct {
		x int
		y string
	}{}
	v, ok := s.(struct {
		x int
		y string
	})
	fmt.Println(ok, v.x == 0)

	// a different field set must not assert
	fmt.Println(fail(func() {
		var s interface{} = struct{ x int }{}
		fmt.Println(s.(struct {
			x int
			y int
		}))
	}))
	fmt.Println("done")
}

func fail(f func()) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	f()
	return
}
