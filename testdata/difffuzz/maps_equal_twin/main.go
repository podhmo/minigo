package main

// maps.Equal compares values with Go's == at the map's value type: a host
// FileMode from os.Stat equals fs.ModeDir, while values of an `any`
// map compare their dynamic types too (int 2 is not uint32 2).

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
)

func main() {
	dir, err := os.MkdirTemp("", "mapsequal")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		panic(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "d"))
	if err != nil {
		panic(err)
	}
	m := fi.Mode().Type() // fs.ModeDir

	fmt.Println(maps.Equal(map[string]fs.FileMode{"d": m}, map[string]fs.FileMode{"d": fs.ModeDir}))
	fmt.Println(maps.Equal(map[string]fs.FileMode{"d": m}, map[string]fs.FileMode{"d": fs.ModeSymlink}))
	fmt.Println(maps.Equal(map[int]any{1: m}, map[int]any{1: fs.ModeDir}), maps.Equal(map[int]any{1: m}, map[int]any{1: uint32(fs.ModeDir)}))
	fmt.Println(maps.Equal(map[string]any{"a": 2}, map[string]any{"a": uint32(2)}), maps.Equal(map[string]any{"a": 2}, map[string]any{"a": 2}))
	type rec struct{ M fs.FileMode }
	fmt.Println(maps.Equal(map[int]rec{1: {m}}, map[int]rec{1: {fs.ModeDir}}))
}
