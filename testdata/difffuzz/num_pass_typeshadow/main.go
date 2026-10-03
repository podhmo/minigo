package main

import "fmt"

// An inner-block `type` decl must not overwrite an outer decl's
// metadata: after the block ends the outer type resolves again.
func main() {
	type M map[int]int
	{
		type M struct{ X int }
		_ = M{X: 1}
	}
	k := 3
	fmt.Println(M{k: 2}[k])

	type S struct{ A int }
	{
		type S struct{ B int }
		fmt.Println(S{B: 4}.B)
	}
	fmt.Println(S{A: 5}.A)
}
