package main

import "fmt"

type S struct{ a int }
type T struct{ a int }

func main() {
	s := S{1}
	// a named struct compares against its anonymous spelling, like a
	// named array — the identical-underlying rule.
	fmt.Println(s == struct{ a int }{1})
	fmt.Println(S{1} == S{1})
	// interface pairs stay strict: the dynamic types differ.
	fmt.Println(any(s) == any(struct{ a int }{1}))
	fmt.Println(any(s) == any(T{1}))
}
