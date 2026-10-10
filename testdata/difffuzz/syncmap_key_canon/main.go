package main

// opaqueStoreArg crossed script values into `any` storage verbatim:
// struct keys compared by pointer identity (two equal K{1} values never
// hit) and unhashable payloads passed the boundary silently where gc's
// interface boxing panics. Key positions (sync.Map keys, context keys,
// CompareAndSwap comparands) now canonicalize like CanonicalKey —
// equal content hashes equal, unhashable payloads panic — while free
// value positions still cross verbatim when unhashable.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

type K struct{ N int }
type Inner struct{ A, B int }
type Celsius float64
type MyInt int

func try(name string, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: panic: %v\n", name, r)
		}
	}()
	fmt.Printf("%s: %v\n", name, f())
}

func main() {
	var m sync.Map
	// struct keys hash by content like gc's interface equality
	m.Store(K{1}, "k1")
	try("struct-load", func() any { v, ok := m.Load(K{1}); return fmt.Sprint(v, ok) })
	// an array key hashes by content too
	m.Store([3]int{1, 2, 3}, "arr")
	try("array-load", func() any { v, ok := m.Load([3]int{1, 2, 3}); return fmt.Sprint(v, ok) })
	// named payloads keep their tag: int(9) is not MyInt(9)
	m.Store(MyInt(9), "mi")
	try("named-key", func() any { v, ok := m.Load(MyInt(9)); return fmt.Sprint(v, ok) })
	try("tag-sep", func() any { _, ok := m.Load(int(9)); return ok })
	// pointers keep identity
	i := Inner{1, 2}
	m.Store(&i, "ptr")
	try("ptr", func() any { v, ok := m.Load(&i); return fmt.Sprint(v, ok) })
	try("ptr-other", func() any { _, ok := m.Load(&Inner{1, 2}); return ok })
	// unhashable keys panic like gc's hash check
	try("slice-key", func() any { m.Store([]int{1}, "v"); return "stored" })
	try("map-key", func() any { m.Store(map[string]int{}, "v"); return "stored" })
	try("func-key", func() any { m.Store(func() {}, "v"); return "stored" })
	try("nilslice-key", func() any { var s []int; m.Store(s, "v"); return "stored" })
	// an unhashable VALUE is legal — only keys run the check
	try("slice-value", func() any {
		m.Store("sv", []int{1})
		v, ok := m.Load("sv")
		return fmt.Sprintf("%v %v", v, ok)
	})
	// a CAS comparand equals a stored value of the same content
	m.Store("c", Inner{5, 6})
	try("cas-eq", func() any { return m.CompareAndSwap("c", Inner{5, 6}, Inner{7, 8}) })
	try("cas-after", func() any { v, _ := m.Load("c"); return fmt.Sprintf("%T %v", v, v) })
	try("cas-neq", func() any { return m.CompareAndSwap("c", Inner{0, 0}, Inner{9, 9}) })
	// Range hands back the original script key, not the key form
	m.Range(func(k, v any) bool {
		if kk, isK := k.(K); isK {
			fmt.Println("range key:", kk.N)
		}
		return true
	})
	m.Delete(K{1})
	try("deleted", func() any { _, ok := m.Load(K{1}); return ok })

	// context keys canonicalize the same way: a second equal instance
	// hits, and an unhashable key panics "key is not comparable".
	ctx := context.WithValue(context.Background(), K{7}, "cv")
	try("ctx-hit", func() any { return ctx.Value(K{7}) })
	try("ctx-miss", func() any { return ctx.Value(K{8}) })
	try("ctx-unhashable", func() any {
		c := context.WithValue(context.Background(), []int{1}, "v")
		return c.Value([]int{1})
	})

	// atomic.Value's CAS comparand takes the same canonical form
	var av atomic.Value
	av.Store(Inner{1, 2})
	try("atomic-cas", func() any { return av.CompareAndSwap(Inner{1, 2}, Inner{3, 4}) })
	try("atomic-after", func() any { return av.Load() })
}
