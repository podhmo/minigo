package main

// --src io/fs: bound os hands host-produced []fs.DirEntry values to the
// source-interpreted io/fs, whose []DirEntry slot takes them by
// same-spelled-twin retag (the tag is host-minted, Spec == nil). The
// gate must not let script-declared same-named types unify — a
// script tag carries a Spec.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	d, err := os.MkdirTemp("", "twins")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(d)
	os.WriteFile(filepath.Join(d, "b.txt"), []byte("b"), 0o644)
	os.WriteFile(filepath.Join(d, "a.txt"), []byte("a"), 0o644)
	ents, err := fs.ReadDir(os.DirFS(d), ".")
	fmt.Println(len(ents), err)
	for _, e := range ents {
		fmt.Println(e.Name(), e.IsDir())
	}
	g, err := fs.Glob(os.DirFS(d), "*.txt")
	fmt.Println(g, err)
}
