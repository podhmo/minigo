package main

// opaqueStoreArg crossed *runtime.Named pointers verbatim into `any`
// storage, so sync.Map keys compared by pointer identity: two equal
// uint16(8) conversions never hit (archive/zip's decompressor registry
// lost every entry and TestParseZipFS failed). Named values now cross
// by value — Go's (type, payload) interface equality — and const-domain
// payloads materialize first; goValueOf re-wraps loaded Nameds.

import (
	"fmt"
	"sync"
)

type myFunc func(int) int

func add1(x int) int { return x + 1 }

func main() {
	var m sync.Map
	m.Store(uint16(8), "u16")
	v, ok := m.Load(uint16(8))
	fmt.Println("u16 key:", v, ok)
	a := uint16(10)
	m.Store(a, "viaVar")
	fmt.Println("var key:", mustLoad(m, a))
	m.Store(int(7), "intExpr")
	fmt.Println("int key:", mustLoad(m, int(7)))
	m.Store(uint16(8), uint16(42))
	fmt.Printf("tag kept: %T\n", mustLoad(m, uint16(8)))
	m.Store("f", myFunc(add1))
	if f2, isMyFunc := mustLoad(m, "f").(myFunc); isMyFunc {
		fmt.Println("named func assert:", f2(1))
	}
}

func mustLoad(m sync.Map, k any) any {
	v, _ := m.Load(k)
	return v
}
