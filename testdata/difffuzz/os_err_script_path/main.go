package main

// os and filepath errors report the path as the script spelled it, not
// the engine's absolute resolution against its virtual cwd.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	dir, err := os.MkdirTemp("", "ospath")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chdir(dir); err != nil {
		panic(err)
	}
	os.MkdirAll(filepath.Join("a", "locked", "in"), 0o755)
	os.WriteFile(filepath.Join("a", "f.txt"), []byte("x"), 0o644)

	_, err = os.ReadFile("nope")
	fmt.Println(err)
	_, err = os.Stat("./nope/x")
	var pe *fs.PathError
	fmt.Println(errors.As(err, &pe), pe.Op, pe.Path, errors.Is(err, fs.ErrNotExist))
	_, err = os.ReadDir("nope")
	fmt.Println(err)
	_, err = os.Open("a/../nope")
	fmt.Println(err)
	fmt.Println(os.Mkdir("a", 0o755))
	fmt.Println(os.Remove("nope"))
	fmt.Println(os.WriteFile("nope/f", nil, 0o644))
	fmt.Println(os.Rename("nope", "other"))
	fmt.Println(os.Rename("a", "./a/sub/x"))
	fmt.Println(os.MkdirAll("a/f.txt/sub/x", 0o755))
	fmt.Println(os.MkdirAll("./a/f.txt/sub", 0o755))
	fmt.Println(os.MkdirAll(filepath.Join(dir, "a/f.txt/sub/x"), 0o755) != nil)
	fmt.Println(os.Chdir("nope"))
	fmt.Println(os.Chdir("a/f.txt"))
	_, err = os.OpenFile("nope/x", os.O_RDONLY, 0)
	fmt.Println(err)

	os.Chmod(filepath.Join("a", "locked"), 0)
	defer os.Chmod(filepath.Join(dir, "a", "locked"), 0o755)
	filepath.WalkDir("a", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			fmt.Println("walkdir", p, err)
		}
		return nil
	})
	err = filepath.WalkDir("nope", func(p string, d fs.DirEntry, err error) error {
		fmt.Println("cb", p, d == nil, err)
		return err
	})
	fmt.Println("ret", err)
	err = filepath.Walk("nope", func(p string, info fs.FileInfo, err error) error {
		return err
	})
	fmt.Println("walk ret", err)
}
