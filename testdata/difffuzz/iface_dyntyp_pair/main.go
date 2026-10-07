package main

import "fmt"

// Interface == compares (dynamic type, value) pairs. typeOfValue
// fabricated anonymous typedefs for pointers (*T) and funcs without a
// spelling AST, so TypIdentical judged them never-identical — the pair
// guard returned false before values compared. Interface-stored
// pointers could never be equal (issue18595) and uncomparable func
// pairs never panicked (issue76008); identical dynamic types also
// panic on the type alone across nil/live wrapper shapes.

type I interface{ M() }
type T struct{}

func (*T) M() {}

func main() {
	t := new(T)
	var i1, i2, i3 I
	var j interface{ M() }
	i1 = t
	j = t
	i2 = j
	i3 = t
	fmt.Println(i1 == i1, i1 == i2, i1 == i3, i2 == j, t == i1)
	var a, b any = t, t
	fmt.Println(a == b)

	f1, f2 := func() {}, func(i int) {}
	fmt.Println(any(f1) == any(f2)) // different signatures: false

	var nf func()
	fmt.Println(nf == nil, any(nf) == nil)

	// panics: identical uncomparable dynamic types
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("func pair:", r)
			}
		}()
		fmt.Println(any(f1) == any(func() {}))
	}()
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("nil func:", r)
			}
		}()
		fmt.Println(any(f1) == any(nf))
	}()
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("nil map:", r)
			}
		}()
		var m map[int]int
		fmt.Println(any(m) == any(map[int]int{}))
	}()
}
