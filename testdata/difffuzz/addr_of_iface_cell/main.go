package main

import "fmt"

// `%T` on `&` of a slot spells the DECLARED type of the pointee — the
// cell's stamped typedef — not the dynamic type of the stored value.
type I interface{ String() string }
type S struct{ V string }

func (s S) String() string { return s.V }

var i I = S{"x"}
var s S = S{"y"}
var a any = S{"z"}
var p *S = &s

func f(v any) { fmt.Printf("%T\n", &v) }

func main() {
	fmt.Printf("%T %T %T %T\n", &i, &s, &a, &p)
	f(S{"w"})
}
