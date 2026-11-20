package main

import (
	"fmt"
)

// %w misuse prints gc's bad-verb marker: a nil or non-error operand is
// %!w(...), a missing operand %!w(MISSING); a valid error still wraps.
type P struct{}

func main() {
	fmt.Println(fmt.Errorf("x %w", nil))
	fmt.Println(fmt.Errorf("x %w %w", nil))
	fmt.Println(fmt.Errorf("x %w", 3))
	fmt.Println(fmt.Errorf("x %w"))
	fmt.Println(fmt.Errorf("a %w b %w", fmt.Errorf("e1"), fmt.Errorf("e2")))
	var p *P
	fmt.Println(fmt.Errorf("x %w", p))
	fmt.Println(fmt.Errorf("x %w", "s"))
	fmt.Println(fmt.Errorf("%w", fmt.Errorf("ok")))
	fmt.Println(fmt.Errorf("%[1]w %[1]w", fmt.Errorf("e")))
}
