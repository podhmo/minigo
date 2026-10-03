// run
package main

import "fmt"

// deferring a nil function registers fine and panics when the
// deferred call is invoked — like a nil-pointer dereference.

var x = 0

func main() {
	ok := func() (r interface{}) {
		defer func() { r = recover() }()
		f()
		return nil
	}()
	e, isErr := r_toErr(ok)
	fmt.Println(isErr, e)
	fmt.Println("x", x)
}

func r_toErr(v interface{}) (interface{}, bool) {
	if e, ok := v.(error); ok {
		return e.Error(), true
	}
	return nil, false
}

func f() {
	var nilf func()
	defer nilf()
	x = 1
}
