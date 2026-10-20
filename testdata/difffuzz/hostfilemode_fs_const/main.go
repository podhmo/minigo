package main

// A FileMode from the host-bound os meets io/fs interpreted from source:
// the host value stays the host's, yet equals the source constants,
// switches and keys maps on them, still prints and formats through the
// host's String, and still equals os's host constants (#944's sibling).

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	dir, err := os.MkdirTemp("", "hostfilemode")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	sub := filepath.Join(dir, "d")
	if err := os.Mkdir(sub, 0o755); err != nil {
		panic(err)
	}
	fi, err := os.Stat(sub)
	if err != nil {
		panic(err)
	}
	fmt.Println(fi.Mode().IsDir(), fi.Mode().Type() == fs.ModeDir, fi.Mode()&fs.ModeDir != 0)
	switch fi.Mode().Type() {
	case fs.ModeDir:
		fmt.Println("dir")
	default:
		fmt.Println("other")
	}
	m := map[fs.FileMode]string{fs.ModeDir: "dir"}
	fmt.Println(m[fi.Mode().Type()])
	var a, b any = fi.Mode().Type(), fs.ModeDir
	fmt.Println(a == b)
	fmt.Printf("%T\n", fi.Mode().Type())

	// printing goes through the host's String; os constants are host values
	t := fi.Mode().Type()
	fmt.Println(t, t.String())
	fmt.Printf("%s %v\n", t, t)
	fmt.Println(t == os.ModeDir, fi.Mode().Perm() == fi.Mode()&os.ModePerm, any(t) == any(os.ModeDir))
	km := map[fs.FileMode]int{os.ModeDir: 1}
	km[t]++
	km[fs.ModeDir]++
	fmt.Println(km[os.ModeDir], len(km))
	fs.WalkDir(os.DirFS(dir), ".", func(path string, d fs.DirEntry, err error) error {
		fmt.Println(path, d.Type(), d.IsDir(), d.Type() == fs.ModeDir)
		return nil
	})
}
