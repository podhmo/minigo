package main

import (
	"fmt"
	t "time"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_qualforeign_neighbors/inner"
)

// The canonical type identity — path + member — keeps legal neighbors
// legal: an aliased `t.Duration` and inner's synthesized-qualifier
// Duration are the same type, so none of these trap.
func main() {
	var y = inner.Dur
	var d t.Duration
	fmt.Println(d == y)        // zero Duration == Second — false
	fmt.Println(y == t.Second) // same type and value — true
	fmt.Println(y != t.Minute) // same type, different value — true
}
