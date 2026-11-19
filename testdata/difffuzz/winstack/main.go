package main

import "fmt"

// P4-3a: operand stacks are windows into a per-goroutine arena —
// nested calls, arena growth, and panic/recover must leave every
// frame's values intact.

// deep recursion forces repeated arena growth.
func deep(n, acc int) int {
	if n == 0 {
		return acc
	}
	return deep(n-1, acc+n)
}

// values held across a panic/recover inside a nested call.
func mayPanic(x int) (r int) {
	defer func() {
		if p := recover(); p != nil {
			r = -x
		}
	}()
	if x < 0 {
		panic("neg")
	}
	return x * 10
}

// a caller's operands survive a callee's own deep arena use.
func caller() int {
	a := deep(40, 0)
	b := mayPanic(-a)
	return a + b
}

func main() {
	fmt.Println(caller())
	// range-over-func: yield parks the producer's window while the
	// body grows the consumer's stack.
	sum := 0
	for i := range seq {
		sum += deep(i, i)
	}
	fmt.Println(sum)
	fmt.Println(mayPanic(7), mayPanic(-7))
}

func seq(yield func(int) bool) {
	for i := 0; i < 4; i++ {
		if !yield(i) {
			return
		}
	}
}
