package main

import "fmt"

type F struct{ n int }

func (f F) Format(s fmt.State, verb rune) {
	fmt.Fprintf(s, "{F%d}", f.n)
}

func main() {
	// an element's Format output belongs inside the composite's
	// brackets — writing it to the live state emits the fragment early.
	fmt.Printf("%v\n", []F{{1}, {2}})
	fmt.Printf("%v\n", map[string]F{"k": {3}})
	fmt.Printf("%v\n", struct{ A F }{A: F{4}})
	fmt.Printf("%v\n", [][]F{{{5}}, {{6}}})
	fmt.Printf("%+v\n", []F{{7}})
	fmt.Printf("%v\n", F{8})
	fmt.Printf("%v\n", &[]F{{9}})
}
