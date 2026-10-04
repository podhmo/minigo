package main

import (
	"fmt"
	"reflect"

	o "github.com/podhmo/minigo/testdata/difffuzz/display-canon/odd"
)

func cmp[T any](a, b T) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic:", r)
		}
	}()
	var x any = a
	var y any = b
	_ = x == y
}

func main() {
	cmp(map[byte]int{1: 2}, map[byte]int{1: 2})
	cmp(map[rune]int{1: 2}, map[rune]int{1: 2})
	cmp(map[o.T]int{o.T{}: 2}, map[o.T]int{o.T{}: 2})
	cmp(struct{ f []int }{f: []int{1}}, struct{ f []int }{f: []int{1}})

	var s struct{ X int }
	fmt.Printf("anon struct %%T: %T\n", s)
	var a any = [3]byte{1, 2, 3}
	fmt.Printf("array %%T: %T\n", a)
	fmt.Printf("imported %%T: %T\n", o.T{})
	fmt.Println("iface String:", reflect.TypeOf((*interface{ M() })(nil)).Elem())

	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic:", r)
		}
	}()
	m := map[any]int{}
	m[map[byte]int{1: 2}] = 3
}
