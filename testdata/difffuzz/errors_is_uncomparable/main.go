package main

import (
	"errors"
	"fmt"
)

// errors.Is skips `err == target` entirely when the target's dynamic
// type is uncomparable — a same-box uncomparable target still answers
// false where a pointer-identity shortcut would wrongly match.
type U struct{ Xs []int }

func (u U) Error() string { return "u" }

type C struct{ X int }

func (c C) Error() string { return "c" }

func main() {
	var u error = U{[]int{1}}
	var c error = C{1}
	fmt.Println(errors.Is(u, u), errors.Is(u, U{[]int{1}}))
	fmt.Println(errors.Is(c, c), errors.Is(c, C{1}))
	e := errors.New("x")
	fmt.Println(errors.Is(e, e))
	fmt.Println(errors.Is(fmt.Errorf("wrap: %w", u), u),
		errors.Is(fmt.Errorf("wrap: %w", c), c))
}
