package main

import (
	"fmt"
	"io"
	"sync"
)

type Decompressor func(r io.Reader) io.ReadCloser

func newReader(r io.Reader) io.ReadCloser { return nil }

func main() {
	var decompressors sync.Map
	decompressors.Store(8, Decompressor(newReader))
	di, _ := decompressors.Load(8)
	_, ok := di.(Decompressor)
	fmt.Println("assert after round-trip:", ok)
}
