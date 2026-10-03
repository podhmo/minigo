package main

import (
	"fmt"
	"strconv"
)

var v_m_2 map[string]int = map[string]int{"a": 1, "b": 2, "c": 3}

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
	try(0, func() any { c := map[string]int{}; c["a"] = 1; delete(c, "a"); return c })
	try(1, func() any {
		c := map[string]int{}
		for k, v := range v_m_2 {
			c[k] = v
		}
		delete(c, strconv.FormatInt(10, 36))
		return c
	})
	try(2, func() any {
		c := map[string]int{}
		for k, v := range v_m_2 {
			c[k] = v
		}
		delete(c, "nope")
		return c
	})
}
