package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	dir, err := os.MkdirTemp("", "fsprobe")
	if err != nil {
		fmt.Println("mkdirtemp:", err)
		return
	}
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "d", "f.txt"), []byte("x"), 0o644); err != nil {
		return
	}
	if err := os.Chdir(dir); err != nil {
		return
	}
	_, e1 := os.Stat("")
	fmt.Println("stat-empty:", e1)
	_, e2 := os.Lstat("d/f.txt/")
	fmt.Println("lstat-slash:", e2)
	n := 0
	filepath.WalkDir("d/", func(p string, d os.DirEntry, err error) error {
		n++
		return nil
	})
	fmt.Println("walkdir-slash:", n)
}
