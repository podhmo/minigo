package main

import (
	"embed"
	"fmt"
)

// a type alias for embed.FS is the same type — the directive must
// resolve aliases before checking for embed.FS.
type FS = embed.FS

//go:embed hello.txt
var f FS

func main() {
	data, err := f.ReadFile("hello.txt")
	fmt.Printf("%q %v\n", data, err)
}
