package main

import "fmt"

var (
	v_bs_0 []byte = nil
	v_rs_0 []rune = nil
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func main() {
	try(0, func() any { return string(v_bs_0) })
	try(1, func() any { return string(v_rs_0) })
	try(2, func() any { return fmt.Sprintf("%q", string(v_bs_0)) })
}
