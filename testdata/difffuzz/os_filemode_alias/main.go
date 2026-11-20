package main

import (
	"fmt"
	"io/fs"
	"os"
)

func main() {
	// os.FileMode is `type FileMode = fs.FileMode` — one type.
	fmt.Println(os.FileMode(0755), os.ModeDir.IsDir())
	m := os.FileMode(0755)
	fmt.Println(m == fs.FileMode(0755), m.IsDir())
	var f fs.FileMode = os.ModePerm
	fmt.Println(f, os.FileMode(f))
	// a script-declared alias to the same target unifies too
	type MyMode = fs.FileMode
	var mm MyMode = fs.ModeDir | 0600
	fmt.Println(mm.IsDir(), mm == (fs.ModeDir|0600))
}
