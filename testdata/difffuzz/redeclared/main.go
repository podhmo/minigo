package main

import "fmt"

// a task func redeclared silently last-wins where gc rejects the build:
// index-level redeclaration now fails the load like `Default redeclared
// in this block`.
func Default() { fmt.Println("first") }
func Default() { fmt.Println("second") }

func main() { Default() }
