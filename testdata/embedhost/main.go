// Package inspectembed hosts embeds into host-bound types.
package main

import (
	"bufio"
	"bytes"
	"io"
)

// BufWrap promotes host-bound methods through a pointer embed: the
// set must include Buffer's exported methods via reflect.
type BufWrap struct {
	*bytes.Buffer
}

// A declares Read directly — the host-bound embed's promoted members
// sit deeper, so RWWrap's Read resolves to A's declaration.
type A struct{}

func (A) Read([]byte) (int, error) { return 0, nil }

type RWWrap struct {
	*bufio.ReadWriter
	A
}

// Rdr embeds an interface binding carrying only method names: its
// members under-resolve rather than surfacing runtime.TypeDef's own
// methods.
type Rdr struct {
	io.Reader
}

// Reader is the requirement the promoted host Read satisfies —
// its []byte parameter is the reflected []uint8.
type Reader interface {
	Read([]byte) (int, error)
}

func main() {}
