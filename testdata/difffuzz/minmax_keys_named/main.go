package main

// slices.Max/Min hand back an element of the slice and maps.Keys/Values
// the map's own keys and values, so a named element type survives —
// fs.FileMode prints through its String method, main.M stays main.M —
// for source values and for a host FileMode from os.Stat or a host
// time.Month alike.

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type M uint32

func main() {
	dir, err := os.MkdirTemp("", "minmax")
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

	src := []fs.FileMode{fs.ModeSymlink, fs.ModeDir, 0}
	fmt.Println(slices.Min(src), slices.Max(src))
	xs := []fs.FileMode{fs.ModeSymlink, m, 0}
	fmt.Println(slices.Min(xs), slices.Max(xs))
	fmt.Printf("%T %T\n", slices.Max(xs), slices.Max([]M{1, 5}))

	mo := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC).Month()
	fmt.Println(slices.Min([]time.Month{time.December, mo}), slices.Max([]time.Month{time.January, mo}))

	ks := slices.Sorted(maps.Keys(map[fs.FileMode]bool{fs.ModeDir: true, 0: true, fs.ModeSymlink: true}))
	fmt.Printf("%T %v\n", ks, ks)
	hk := slices.Sorted(maps.Keys(map[fs.FileMode]int{m: 1, 0: 2}))
	fmt.Println(hk)
	mk := slices.Sorted(maps.Keys(map[M]bool{4: true, 2: true}))
	fmt.Printf("%T %v\n", mk[0], mk)
	vs := slices.Sorted(maps.Values(map[string]fs.FileMode{"d": m, "l": fs.ModeSymlink}))
	fmt.Println(vs)
}
