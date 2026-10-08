package main

import (
	"embed"
	"fmt"
)

// a..b.txt contains ".." but not as a path element — Go only rejects
// ".." (and ".") as whole elements, not as a substring.
//
//go:embed a..b.txt
var f embed.FS

func main() {
	data, err := f.ReadFile("a..b.txt")
	fmt.Printf("%q %v\n", data, err)
}
