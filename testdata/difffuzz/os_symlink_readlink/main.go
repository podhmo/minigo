package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	dir, err := os.MkdirTemp("", "symprobe")
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
	fmt.Println("symlink:", os.Symlink("f.txt", "d/lnk"))
	st, err := os.Lstat("d/lnk")
	if err != nil {
		fmt.Println("lstat:", err)
		return
	}
	fmt.Println("mode:", st.Mode()&os.ModeSymlink != 0)
	t, err := os.Readlink("d/lnk")
	fmt.Println("readlink:", t, err)
	fmt.Println("dup:", os.Symlink("f.txt", "d/lnk"))
	fmt.Println("bad:", os.Symlink("no", "nodir/lnk"))
	fmt.Println("readlink-miss:", func() any {
		_, e := os.Readlink("d/f.txt")
		return e
	}())
}
