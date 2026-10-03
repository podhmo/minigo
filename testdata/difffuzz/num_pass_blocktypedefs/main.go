package main

import "fmt"

type T struct{ X int }

// A `type` decl inside an inner block dies with the block: later
// decls must see the package-level T again, not the ended local one.
func main() {
	{
		type T struct{ Y int }
		_ = T{}
	}

	type U struct{ T }
	u := U{T: T{X: 7}}
	fmt.Println(u.X)

	// Same name re-declared in a live block still shadows correctly.
	{
		type T struct{ Z int }
		type V struct{ T }
		v := V{T: T{Z: 8}}
		fmt.Println(v.Z)
	}
}
