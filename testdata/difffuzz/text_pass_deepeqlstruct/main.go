package main

import (
	"fmt"
	"reflect"
)

// DeepEqual compares full type identity: anonymous structs differ on
// field types (not just names), and two same-named decls in different
// blocks are distinct types.
func main() {
	fmt.Println(reflect.DeepEqual(
		struct{ X int }{1},
		struct{ X any }{1},
	))
	fmt.Println(reflect.DeepEqual(
		struct{ X int }{1},
		struct{ X int }{1},
	))
	type U struct{ Y int }
	fmt.Println(reflect.DeepEqual(U{1}, U{1}))
	var u1, u2 any
	{
		type T struct{ Y int }
		u1 = T{Y: 1}
	}
	{
		type T struct{ Y int }
		u2 = T{Y: 1}
	}
	fmt.Println(reflect.DeepEqual(u1, u2))
}
