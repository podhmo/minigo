package main

import (
	"fmt"
	"log"
)

// fmt.Append/Appendf/Appendln were unbound, which trapped source-interpreted
// `log` (its output() formats through fmt.Appendln). The log.Println below
// exercises that path end to end — its line goes to stderr, keeping stdout
// deterministic.
func main() {
	b := fmt.Append(nil, "a", 1, "b")
	fmt.Println(string(b))
	b = fmt.Appendf(b, " %d-%s", 42, "x")
	fmt.Println(string(b))
	b = fmt.Appendln(b, "tail", 2)
	fmt.Print(string(b))
	log.Println("marker")
}
