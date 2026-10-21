package main

// filepath.WalkDir/Walk hand the root to the callback as written and
// join descendants onto it; the root entry's Name() is the base of the
// spelling ("." for ".", ".." for "a/b/..").

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	dir, err := os.MkdirTemp("", "walkroot")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chdir(dir); err != nil {
		panic(err)
	}
	os.MkdirAll(filepath.Join("a", "b"), 0o755)
	os.WriteFile(filepath.Join("a", "b", "g.txt"), []byte("x"), 0o644)

	for _, root := range []string{"./a", "a/", "a/b/..", ".", "a/./b"} {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			info, _ := d.Info()
			fmt.Printf("D %q %q %q %v\n", p, d.Name(), info.Name(), err)
			return nil
		})
		filepath.Walk(root, func(p string, info fs.FileInfo, err error) error {
			fmt.Printf("W %q %q %v\n", p, info.Name(), err)
			return nil
		})
	}
	filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if p == "." {
			fmt.Println(d)
			return nil
		}
		return fs.SkipAll
	})
}
