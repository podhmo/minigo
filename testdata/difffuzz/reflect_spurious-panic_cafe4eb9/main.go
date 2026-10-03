package main

import (
	"fmt"
	"reflect"
)

type RInner struct{ X int }
type ROuter struct {
	RInner
	B int
}
type RTag struct {
	A int    `json:"a"`
	B string `json:"b,omitempty"`
}
type RNamed int
type RHidden struct {
	x int
	Y int
}

var (
	r_i0     = 42
	r_s0     = "hi"
	r_f0     = 1.5
	r_b0     = true
	r_slice  = []int{3, 4, 5}
	r_arr    = [3]int{1, 2, 3}
	r_map    = map[string]int{"a": 1, "b": 2}
	r_struct = RTag{A: 7, B: "t"}
	r_outer  = ROuter{RInner: RInner{X: 9}, B: 8}
	r_hidden = RHidden{x: 1, Y: 2}
	r_named  = RNamed(5)
	r_iface  = any("iface")
	r_nilptr = (*int)(nil)
	r_nilmap = map[string]int(nil)
	r_bytes  = []byte("abc")
	r_chan   = make(chan int, 1)
	r_func   = func(x int) int { return x * 2 }
	r_funcv  = func(xs ...int) int {
		s := 0
		for _, x := range xs {
			s += x
		}
		return s
	}
	r_empty    = any(nil)
	r_strslice = []string{"p", "q"}
	r_sptr     = &r_struct
)

var ()

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func id[T any](x T) T { return x }

func main() {
	try(0, func() any { v0 := reflect.TypeOf(r_funcv); v1 := v0.In(0); v2 := v1.Kind(); return v2 })
}
