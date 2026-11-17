package main

import "fmt"

// len(a) over a type parameter is not a constant, so 1<<len(a) folds
// at runtime and uint8(256) is 0 — a const fold would trap.
func h[T ~[8]int](a T) uint8 { return 1 << len(a) }

func main() { fmt.Println(h([8]int{})) }
