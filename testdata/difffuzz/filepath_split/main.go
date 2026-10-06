package main

import (
	"fmt"
	"path/filepath"
)

// filepath.Split was missing from the bound path/filepath, so go/scanner's
// Init (and every go/parser.ParseFile) trapped `undefined: filepath.Split`.
func main() {
	for _, p := range []string{"a/b/c.go", "c.go", "a/b/", "/", "", "/root.txt", "dir/日本語.txt", "a//b"} {
		dir, file := filepath.Split(p)
		fmt.Printf("%q -> %q %q\n", p, dir, file)
	}
	_, base := filepath.Split("pkg/api/api.go")
	fmt.Println(base == filepath.Base("pkg/api/api.go"))
}
