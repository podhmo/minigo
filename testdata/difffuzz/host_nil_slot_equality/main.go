package main

// Typed nils from the host (os.Open's nil *os.File, a sync.Map-loaded
// typed nil, context's nil Done chan, net.ParseIP's nil IP) against the
// script's own nils in interface-typed slots: struct and array ==,
// slices/maps helpers, map[any] keys, interface ==, reflect.DeepEqual
// and type switches. A host nil *os.File keys a map[any] like the
// script's (*os.File)(nil); a nil net.IP DeepEquals a zero net.IP.

import (
	"context"
	"fmt"
	"maps"
	"net"
	"os"
	"reflect"
	"slices"
	"sync"
)

type T struct{ n int }

type S struct{ A any }

type E struct{ F *os.File }

func main() {
	f, _ := os.Open("/nonexistent")
	var g *os.File
	var t *T
	var m sync.Map
	m.Store("f", f)
	m.Store("t", t)
	lf, _ := m.Load("f")
	lt, _ := m.Load("t")

	// interface-typed struct fields
	fmt.Println(S{f} == S{nil}, S{f} == S{g}, S{f} == S{(*os.File)(nil)}, S{lf} == S{nil}, S{lf} == S{g})
	fmt.Println(S{t} == S{nil}, S{lt} == S{t}, S{lt} == S{nil}, S{nil} == S{})
	fmt.Println(E{f} == E{}, E{f} == E{g})
	// arrays of any
	fmt.Println([2]any{f, nil} == [2]any{g, nil}, [1]any{lf} == [1]any{nil}, [1]any{lt} == [1]any{t})
	// slices / maps helpers
	fmt.Println(slices.Equal([]any{f}, []any{nil}), slices.Equal([]any{f}, []any{g}), slices.Contains([]any{lt}, any(nil)), slices.Index([]any{nil, lt}, any(t)))
	fmt.Println(maps.Equal(map[string]any{"a": f}, map[string]any{"a": nil}), maps.Equal(map[string]any{"a": lf}, map[string]any{"a": g}))
	fmt.Println(maps.Equal(map[string]*os.File{"a": f}, map[string]*os.File{"a": nil}))
	// map[any] keys
	mk := map[any]int{}
	mk[f] = 1
	mk[g] = 2
	mk[nil] = 3
	mk[t] = 4
	fmt.Println(len(mk), mk[f], mk[nil], mk[lf], mk[lt])
	// interface ==
	var x, y any = f, g
	fmt.Println(x == y, x == nil, lf == x, lt == any(t), lt == nil)
	// DeepEqual
	fmt.Println(reflect.DeepEqual(S{f}, S{nil}), reflect.DeepEqual(S{f}, S{g}), reflect.DeepEqual([]any{lt}, []any{t}))
	// chan/func nils
	d := context.Background().Done()
	fmt.Println(S{d} == S{nil}, S{d} == S{(<-chan struct{})(nil)}, slices.Contains([]any{d}, any(nil)))
	// net.IP nil in any slot (uncomparable type -> only nil compare is legal; check via DeepEqual)
	ip := net.ParseIP("bad")
	var mine net.IP
	fmt.Println(reflect.DeepEqual(S{ip}, S{mine}), reflect.DeepEqual(S{ip}, S{nil}), reflect.DeepEqual(ip, mine))
	// switch over typed nil in any
	for _, v := range []any{f, lf, lt, d, nil, ip} {
		switch v.(type) {
		case nil:
			fmt.Print("nil ")
		case *os.File:
			fmt.Print("file ")
		case *T:
			fmt.Print("T ")
		case <-chan struct{}:
			fmt.Print("chan ")
		case net.IP:
			fmt.Print("ip ")
		}
	}
	fmt.Println()
}
