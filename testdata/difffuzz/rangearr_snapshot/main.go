package main

import "fmt"

// Array range evaluates the operand once and iterates the copy;
// pointer-to-array operands iterate the live pointee. A second LHS
// operand (even `_`) makes `range *p` evaluate `*p` eagerly — nil
// dereferences panic and the copy keeps later writes invisible.

type A [3]int

func try(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println(name, "panic")
		}
	}()
	f()
	fmt.Println(name, "ok")
}

func main() {
	a := [3]int{1, 2, 3}
	out := []int{}
	for _, v := range a {
		a[1] = 99 // copy hides it
		out = append(out, v)
	}
	fmt.Println("arr", out)
	a = [3]int{1, 2, 3}
	p := &a
	out = out[:0]
	for _, v := range p {
		p[1] = 99 // live: printed
		out = append(out, v)
	}
	fmt.Println("ptr", out)
	out = out[:0]
	for _, v := range *p {
		p[1] = 77 // copy hides it
		out = append(out, v)
	}
	fmt.Println("star", out)
	na := A{4, 5, 6}
	out = out[:0]
	for _, v := range na {
		na[1] = 55
		out = append(out, v)
	}
	fmt.Println("named", out)
	var n0 *[0]int
	try("star0-elem", func() {
		for _, v := range *n0 {
			_ = v
		}
	})
	try("star0-blank", func() {
		for i, _ := range *n0 {
			_ = i
		}
	})
	var n3 *[3]int
	try("ptr-elem", func() {
		for i, v := range n3 {
			_, _ = i, v
		}
	})
	try("ptr0-elem", func() {
		for i, v := range n0 { // zero elements: never reads — no panic
			_, _ = i, v
		}
	})
}
