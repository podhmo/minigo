package main

// A FileMode from the host-bound os stays io/fs's FileMode when nested
// or generic: struct and array equality, struct/array/any map keys,
// inferred type parameters and the slices helpers all meet the source
// constants (hostfilemode_fs_const covers the operand-level cases).

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

type rec struct{ M fs.FileMode }

type pair struct {
	M fs.FileMode
	N int
}

func eq[T comparable](a, b T) bool { return a == b }

func id[T any](x T) T { return x }

func main() {
	dir, err := os.MkdirTemp("", "hostfilemode")
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
	t := fi.Mode().Type()
	ents, err := os.ReadDir(dir)
	if err != nil {
		panic(err)
	}
	d := ents[0].Type() // a DirEntry's FileMode
	for _, m := range []fs.FileMode{t, d} {
		fmt.Println("struct", rec{m} == rec{fs.ModeDir}, rec{fs.ModeDir} == rec{m}, rec{m} != rec{fs.ModeSymlink}, pair{m, 1} == pair{fs.ModeDir, 1})
		fmt.Println("array", [1]fs.FileMode{m} == [1]fs.FileMode{fs.ModeDir}, [2]fs.FileMode{0, m} == [2]fs.FileMode{0, fs.ModeDir})
		var ai, bi any = rec{m}, rec{fs.ModeDir}
		fmt.Println("any(struct)", ai == bi)

		sm := map[rec]int{{fs.ModeDir}: 1}
		fmt.Println("struct key", sm[rec{m}])
		sm[rec{m}]++
		fmt.Println(len(sm), sm[rec{fs.ModeDir}])
		am := map[[1]fs.FileMode]int{{fs.ModeDir}: 1}
		fmt.Println("array key", am[[1]fs.FileMode{m}])
		anym := map[any]int{fs.ModeDir: 1}
		fmt.Println("any key", anym[m])
		anym[m]++
		fmt.Println(len(anym), anym[fs.ModeDir])

		fmt.Println("generic", eq(m, fs.ModeDir), eq(fs.ModeDir, m), eq[fs.FileMode](m, fs.ModeDir), id(m) == fs.ModeDir)
		fmt.Printf("%T %v\n", id(m), id(m))
		xs := []fs.FileMode{0, fs.ModeDir}
		fmt.Println("slices", slices.Contains(xs, m), slices.Index(xs, m), slices.Equal(xs, []fs.FileMode{0, m}))
		ys := []fs.FileMode{m}
		fmt.Println(slices.Contains(ys, fs.ModeDir), slices.Index(ys, fs.ModeDir), slices.Index(ys, fs.ModeSymlink))
	}
}
