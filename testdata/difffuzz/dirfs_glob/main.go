package main

import (
	"fmt"
	"io/fs"
	"os"
)

func main() {
	fsys := os.DirFS("/nonexistent-minigo-dirfs")
	m, err := fs.Glob(fsys, "*.tmpl")
	fmt.Println(m, err)
	b, err := fs.ReadFile(fsys, "a.tmpl")
	fmt.Println(string(b), err)
}
