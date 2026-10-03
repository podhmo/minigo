// run
package main

import "fmt"

// recovered panic payloads must satisfy `.(error)` like Go's
// runtime.Error / TypeAssertionError, and uncomparable/unhashable
// checks fire on the TYPE even for nil values.

type T struct {
	a, b int
	c    []int
}

func main() {
	// a failed type assertion panics with an error value
	fmt.Println(recovered(func() {
		var x interface{} = 1
		fmt.Println(x.(float32))
	}))
	// comparing an interface whose dynamic type is uncomparable
	fmt.Println(recovered(func() {
		var x T
		var z interface{} = x
		fmt.Println(z != z)
	}))
	// hashing the same unhashable struct as a map key
	fmt.Println(recovered(func() {
		var x T
		m := make(map[interface{}]int)
		m[x] = 1
	}))
	// a nil slice key is still unhashable (the type decides)
	fmt.Println(recovered(func() {
		var s []int
		var z interface{} = s
		m := make(map[interface{}]int)
		m[z] = 1
	}))
	fmt.Println("done")
}

func recovered(f func()) string {
	v := catch(f)
	if e, ok := v.(error); ok {
		return e.Error()
	}
	return "no error"
}

func catch(f func()) (v interface{}) {
	defer func() { v = recover() }()
	f()
	return nil
}
