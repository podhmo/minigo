package main

// A host named basic (fs.FileMode from the host os, reflect.Kind,
// time.Month off a time.Time) orders among the source values of its
// type: slices.Sort/IsSorted/Compact/BinarySearch and the sort package
// see its payload, as in the go toolchain.

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"
)

func main() {
	dir, err := os.MkdirTemp("", "hostnamed")
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

	xs := []fs.FileMode{fs.ModeSymlink, m, 0, fs.ModeDir}
	slices.Sort(xs)
	fmt.Println(xs, slices.IsSorted(xs))
	fmt.Println(slices.Compact(xs))
	fmt.Println(slices.BinarySearch([]fs.FileMode{0, m, fs.ModeSymlink}, fs.ModeDir))
	fmt.Println(slices.BinarySearch([]fs.FileMode{0, fs.ModeDir, fs.ModeSymlink}, m))
	fmt.Println(slices.IsSorted([]fs.FileMode{m, fs.ModeSymlink}), slices.IsSorted([]fs.FileMode{fs.ModeSymlink, m}))
	ks := map[fs.FileMode]bool{m: true, 0: true, fs.ModeSymlink: true}
	sorted := slices.Sorted(maps.Keys(ks))
	fmt.Println(len(sorted), sorted[0] == 0, sorted[2] == fs.ModeDir)

	k := reflect.TypeOf(1).Kind()
	kinds := []reflect.Kind{reflect.String, k, reflect.Bool}
	slices.Sort(kinds)
	fmt.Println(kinds)

	mo := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC).Month()
	mos := []time.Month{time.December, mo, time.January}
	slices.Sort(mos)
	fmt.Println(mos, slices.Compact([]time.Month{mo, time.March, time.April}))
}
