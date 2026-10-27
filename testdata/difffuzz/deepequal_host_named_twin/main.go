package main

// reflect.DeepEqual meets a host named basic (the fs.FileMode os.Stat
// returns from the host-bound os) and its source twin's value (io/fs's
// ModeDir, interpreted from source) as the same type, at the top level
// and inside containers. A different type with the same payload
// (uint32) stays unequal.

import (
	"fmt"
	"io/fs"
	"os"
	"reflect"
)

func main() {
	dir, _ := os.MkdirTemp("", "deq")
	defer os.RemoveAll(dir)
	st, _ := os.Stat(dir)
	m := st.Mode().Type()
	fmt.Println(reflect.DeepEqual(m, fs.ModeDir), reflect.DeepEqual(fs.ModeDir, m))
	fmt.Println(reflect.DeepEqual([]fs.FileMode{m}, []fs.FileMode{fs.ModeDir}))
	fmt.Println(reflect.DeepEqual(map[string]fs.FileMode{"a": m}, map[string]fs.FileMode{"a": fs.ModeDir}))
	fmt.Println(reflect.DeepEqual(fs.FileMode(1), uint32(1)), reflect.DeepEqual(m, uint32(fs.ModeDir)))
	fmt.Println(reflect.DeepEqual(m, fs.ModePerm), reflect.DeepEqual(st.Mode().Perm(), fs.FileMode(0o700)))
	fmt.Println(reflect.DeepEqual(st.Mode().IsDir(), true))
}
