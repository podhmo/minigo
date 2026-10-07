package main

import "fmt"

type Duration int64

const Second Duration = 1e9

func main() {
	// `x = lit` re-types into the variable's inferred type — a named
	// basic keeps its tag across plain assignment.
	d := Second
	d = 5
	fmt.Printf("%T %v\n", d, d)

	x := int8(1)
	x = 100
	fmt.Printf("%T %v\n", x, x)

	// bare scalars materialize under the inferred type too.
	f := 1.5
	f = 2
	fmt.Printf("%T %v\n", f, f)

	s := "a"
	s = "b"
	fmt.Printf("%T %v\n", s, s)

	// an interface-typed := stays loose — the cell's declared type is
	// `any`, not the tag of the value it happens to hold.
	i := any(int8(5))
	i = 300
	fmt.Printf("%T %v\n", i, i)

	// the mark chains through another := and a declared `var`.
	i2 := i
	i2 = "str"
	fmt.Printf("%T %v\n", i2, i2)

	// `x := v.(T)` declares T.
	i = int8(5)
	y := i.(int8)
	y = 9
	fmt.Printf("%T %v\n", y, y)

	// composites and &literals stamp their declared type.
	st := struct{ a int }{a: 1}
	st = struct{ a int }{a: 2}
	fmt.Println(st)
	p := &struct{ a int }{a: 3}
	p = &struct{ a int }{a: 4}
	fmt.Println(*p)
}
