package main

import (
	"bytes"
	"fmt"
	"strings"
	"sync/atomic"
)

// Assignment of a HostNew-typed var copies the host struct like gc:
// the box is a `*T` whose declared type is the struct itself, so
// `b := a` snapshots while `p := &a` keeps sharing. Method receivers
// still reach the real box (a pointer-typed coerce, not a copy).
func main() {
	var a bytes.Buffer
	b := a
	b.WriteString("x")
	fmt.Println(a.Len(), b.Len())

	var s strings.Builder
	t := s
	t.WriteString("y")
	fmt.Println(s.Len(), t.Len())

	var p *bytes.Buffer = &a
	p.WriteString("z")
	fmt.Println(a.Len(), p.Len())

	var at atomic.Int64
	at.Store(5)
	at2 := at
	at2.Store(9)
	fmt.Println(at.Load(), at2.Load())

	a.WriteString("w")
	fmt.Println(a.Len(), a.String())
}
