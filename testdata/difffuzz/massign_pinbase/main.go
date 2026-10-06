package main

// gc evaluates a multi-assign's pointer-indirection operands before any
// store: `p, p.i = new(T), v` writes i through the OLD p (the implicit
// indirection operand pins like *p does), and `p, p.i = nil, v` through
// a still-live p does not panic.

import "fmt"

func main() {
	{
		type T struct{ i int }
		var x T
		p := &x
		p, p.i = new(T), 4
		fmt.Println(x.i, p.i) // 4 0
	}
	{
		type T struct{ x struct{ y int } }
		var x T
		p := &x
		p, p.x.y = new(T), 7
		fmt.Println(x.x.y, p.x.y) // 7 0
	}
	{
		type T *struct{ x struct{ y int } }
		x := struct{ y int }{0}
		var q T = &struct{ x struct{ y int } }{x}
		p := q
		p, p.x.y = nil, 9  // operand read while p == q: no panic
		fmt.Println(q.x.y) // 9
	}
}
