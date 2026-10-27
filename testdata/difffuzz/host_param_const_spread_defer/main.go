// Constant args to host calls next to variadic spreads, method values,
// func values and deferred bound calls convert at the parameter type.
package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

type T struct{}

func (T) Sum(base uint64, xs ...uint64) uint64 {
	for _, x := range xs {
		base += x
	}
	return base
}

func main() {
	xs := []any{1, "a", 1 << 40}
	fmt.Println(xs...)
	fmt.Println(1<<62, xs)
	parts := []string{"a", "b", "c"}
	fmt.Println(filepath.Join(parts...), filepath.Join("x", "y"))
	fmt.Println(strings.Join(parts, "-"), strings.NewReplacer("a", "1", "b", "2").Replace("abc"))
	var u atomic.Uint64
	defer func() { fmt.Println(u.Load()) }()
	u.Store(1<<64 - 1)
	f := u.CompareAndSwap
	fmt.Println(f(1<<64-1, 3), u.Load())
	g := strconv.FormatUint
	fmt.Println(g(1<<63, 2))
	defer fmt.Println(strconv.FormatUint(1<<64-1, 16))
	defer u.Store(1<<64 - 2)
	defer strconv.FormatUint(1<<63, 10)
	fmt.Println(T{}.Sum(1<<63, 1, 2), T.Sum(T{}, 1<<63, []uint64{1, 2}...))
	fmt.Printf("%d %v\n", []any{1 << 62, 2}...)
}
