package main

// A typed nil pointer stored in a host `any` container (sync.Map,
// context.Value, atomic.Value) is a non-nil interface: it
// loads back with its dynamic type, so `v == nil` is false and `v.(*T)`
// matches. Covers a host-origin nil *os.File (os.Open on error), a
// script `var f *os.File`, a script *T, and nil slices/maps for
// contrast; untyped nil and a nil error still load as nil.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
)

type key struct{}

type T struct{ n int }

func (t *T) Name() string {
	if t == nil {
		return "nil T"
	}
	return "T"
}

func main() {
	g, _ := os.Open("/nonexistent")
	var hm sync.Map
	hm.Store("b", g)
	hv, _ := hm.Load("b")
	fmt.Println(hv == nil)
	fmt.Printf("%T\n", hv)
	fmt.Println(context.WithValue(context.Background(), key{}, g).Value(key{}) == nil)
	var hav atomic.Value
	hav.Store(g)
	fmt.Println(hav.Load() == nil)

	var m sync.Map
	var t *T
	var f *os.File
	var s []int
	var mp map[string]int
	m.Store("nil", nil)
	m.Store("t", t)
	m.Store("f", f)
	m.Store("s", s)
	m.Store("mp", mp)
	var x any = t
	m.Store("x", x)
	var e error
	m.Store("e", e)
	keys := []string{}
	m.Range(func(k, v any) bool {
		keys = append(keys, fmt.Sprintf("%v:%v:%T", k, v == nil, v))
		return true
	})
	sort.Strings(keys)
	fmt.Println(keys)
	for _, k := range []string{"nil", "t", "f", "s", "mp", "x", "e"} {
		v, ok := m.Load(k)
		fmt.Println(k, v == nil, ok)
	}
	v, _ := m.Load("t")
	tt, ok := v.(*T)
	fmt.Println(tt == nil, ok)
	fmt.Println(v.(interface{ Name() string }).Name())
	fmt.Println(v == any(t), v == any(f))
	v2, _ := m.Load("f")
	ff, ok := v2.(*os.File)
	fmt.Println(ff == nil, ok)
	old, loaded := m.LoadOrStore("t", 1)
	fmt.Println(old == nil, loaded)
	old, loaded = m.LoadAndDelete("f")
	fmt.Println(old == nil, loaded)
	old, loaded = m.Swap("t", nil)
	fmt.Println(old == nil, loaded)

	var av atomic.Value
	av.Store(t)
	fmt.Println(av.Load() == nil)
	fmt.Println(av.Swap(t) == nil)
	fmt.Println(av.CompareAndSwap(t, t))
	var p atomic.Pointer[T]
	fmt.Println(p.Load() == nil)

	ctx := context.WithValue(context.Background(), key{}, f)
	fv, ok := ctx.Value(key{}).(*os.File)
	fmt.Println(fv == nil, ok)
	fmt.Println(context.WithValue(context.Background(), key{}, t).Value(key{}) == nil)
	fmt.Println(ctx.Value("other") == nil)
}
