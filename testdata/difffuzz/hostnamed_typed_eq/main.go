package main

// A boxed host named basic (fs.FileMode from the host os, reflect.Kind,
// time.Month/Weekday off a time.Time) meets its own type's values only:
// in an `any` struct field, an interface comparison, an `any` map key,
// a struct key with an `any` field and a []any search, a same-payload
// value of another type stays apart, as in the go toolchain.
// hostfilemode_nested pins the twin-typed slots that must still meet.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"
)

type S struct{ A any }

type rec struct{ M fs.FileMode }

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
	fh, err := os.Create(filepath.Join(dir, "f"))
	if err != nil {
		panic(err)
	}
	if err := fh.Chmod(0o002); err != nil {
		panic(err)
	}
	fh.Close()
	fi2, err := os.Stat(filepath.Join(dir, "f"))
	if err != nil {
		panic(err)
	}
	p := fi2.Mode().Perm()        // FileMode(2)
	k := reflect.TypeOf(1).Kind() // reflect.Int == 2
	t := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
	mo, wd := t.Month(), t.Weekday() // 3, 5
	fmt.Println(m, p, k, mo, wd)

	fmt.Println("field", S{m} == S{uint32(fs.ModeDir)}, S{k} == S{2}, S{m} == S{fs.ModeDir}, rec{m} == rec{fs.ModeDir})
	var a, b any = p, k
	fmt.Println("iface", a == b, a == any(fs.FileMode(2)), S{mo} == S{3}, S{mo} == S{time.March}, S{wd} == S{5})

	mm := map[any]int{}
	mm[m] = 1
	mm[uint32(fs.ModeDir)] = 2
	mm[int64(fs.ModeDir)] = 3
	fmt.Println("any key", len(mm), mm[m], mm[fs.ModeDir])
	fmt.Println(len(map[any]string{p: "mode", k: "kind"}))
	tm := map[any]int{mo: 1, 3: 2, wd: 3, 5: 4}
	fmt.Println(len(tm), tm[time.March], tm[mo], tm[3], tm[wd], tm[5])
	hit := map[any]int{fs.ModeDir: 1}
	fmt.Println(hit[m], hit[uint32(fs.ModeDir)])

	type K struct{ A any }
	km := map[K]int{{m}: 1, {uint32(fs.ModeDir)}: 2}
	fmt.Println("struct key", len(km), km[K{fs.ModeDir}])

	fmt.Println("slices", slices.Contains([]any{uint(2), 2}, any(k)), slices.Index([]any{uint(2), 2}, any(k)),
		slices.Equal([]any{k}, []any{2}), slices.Contains([]any{reflect.Int}, any(k)), slices.Index([]any{2, p}, any(fs.FileMode(2))))
	n := 8
	fmt.Println("narrow", slices.Contains([]uint8{0}, 1<<n), slices.Index([]int8{5, -128}, 1<<(n-1)))
}
