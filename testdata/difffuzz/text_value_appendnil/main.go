package main

import (
	"fmt"
)

var (
	v_ss_0 []string = nil
	v_ss_1 []string = []string{}
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
	try(0, func() any { return fmt.Sprintf("%#v", append(v_ss_0, v_ss_1...)) })
}
