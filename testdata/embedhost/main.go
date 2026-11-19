// Package inspectembed hosts embeds into host-bound types.
package main

import "bytes"

// BufWrap promotes host-bound methods through a pointer embed: the
// set must include Buffer's exported methods via reflect.
type BufWrap struct {
	*bytes.Buffer
}

func main() {}
