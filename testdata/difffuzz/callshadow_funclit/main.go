package main

import (
	"fmt"
	"time"
)

// a local func variable shadows the package function of the same name —
// the call resolves its own result type, not the decl's.
func g() int { return 0 }

func main() {
	g := func() time.Duration { return 5 }
	d := time.Duration(1)
	fmt.Println(g() == d)
}
