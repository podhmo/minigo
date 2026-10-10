package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Reader has a niladic, side-effecting method like a json Decoder's
// ReadToken: a container Store boxes the value verbatim (gc's interface
// conversion), it must not invoke the method at marshal time.
type Reader struct {
	calls int
}

func (r *Reader) Next() string {
	r.calls++
	return "tok"
}

type keyT struct{ id string }

func main() {
	var m sync.Map
	r := &Reader{}
	m.Store("r", r)
	got, _ := m.Load("r")
	fmt.Println(got.(*Reader) == r, r.calls)

	// a struct key is comparable in Go; the same value loads back.
	m.Store(r, "key")
	v2, _ := m.Load(r)
	fmt.Println(v2, r.calls)

	var av atomic.Value
	av.Store(r)
	fmt.Println(av.Load().(*Reader) == r, r.calls)

	// context.WithValue kept script args raw already; the Value lookup
	// must box the key the same way or the stored key never matches.
	k := keyT{"x"}
	ctx := context.WithValue(context.Background(), k, r)
	fmt.Println(ctx.Value(k) == r, r.calls)
}
