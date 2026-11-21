package main

// COMPILE_TIME_FACT: a shadowed or same-named predeclared name never mistypes a bind

import (
	"fmt"
	"unsafe"
)

// shadows the predeclared int inside this package — a synthesized
// range-index `int` still names the builtin type
type int interface{ M() }

func main() {
	calls := 0
	g := func() int { calls++; return 1 }
	for i := range []string{"a", "b", "c"} {
		fmt.Println(i)
	}
	// the loop variable shares the builtin's name — Sizeof must still
	// fold from its declared int and never evaluate the operand
	for int := range []string{"x"} {
		_ = unsafe.Sizeof(int + g())
	}
	fmt.Println(calls)
}
