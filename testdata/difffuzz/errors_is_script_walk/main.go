package main

import (
	"errors"
	"fmt"
)

// errors.Is walks a tree containing script errors like gc: `err ==
// target` on comparable targets (canonical value equality for script
// values), a node's own Is(error) bool, then Unwrap() error and
// Unwrap() []error children depth-first.
type E struct{ N int }

func (e E) Error() string { return fmt.Sprintf("e%d", e.N) }

type W struct{ inner error }

func (w W) Error() string { return "w" }
func (w W) Unwrap() error { return w.inner }

type M struct{ errs []error }

func (m M) Error() string   { return "m" }
func (m M) Unwrap() []error { return m.errs }

type C struct{}

func (c C) Error() string { return "c" }
func (c C) Is(t error) bool {
	_, ok := t.(E)
	return ok
}

func main() {
	target := errors.New("t")
	fmt.Println(errors.Is(fmt.Errorf("w: %w", target), target))
	fmt.Println(errors.Is(W{target}, target))
	fmt.Println(errors.Is(M{[]error{errors.New("x"), W{target}}}, target))
	fmt.Println(errors.Is(E{1}, E{1}))
	fmt.Println(errors.Is(E{1}, E{2}))
	fmt.Println(errors.Is(C{}, E{9}))
	fmt.Println(errors.Is(errors.Join(errors.New("a"), target), target))
	fmt.Println(errors.Is(nil, nil), errors.Is(target, nil))
}
