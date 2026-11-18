package main

// A user type named `int` shadows the predeclared one for the whole
// package — slot classification must not let the builtin name hide its
// pointer-receiver methods (P4-2a).
type int struct{ N int }

func (i *int) Bump() { i.N++ }

func Mk() int { return int{N: 1} }

func Use() int {
	v := Mk()
	v.Bump()
	return v
}
