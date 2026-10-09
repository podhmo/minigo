package main

// --src strings,bytes: bound int-returning helpers reached through
// internal/stringslite and internal/bytealg must hand back `int`, not a
// Named{int64} that traps "cannot use int64 as int" at the caller's
// result slot.

import (
	"bytes"
	"fmt"
	"strings"
)

func main() {
	fmt.Println(strings.Index("chicken", "ken"))
	fmt.Println(strings.IndexByte("chicken", 'e'))
	fmt.Println(strings.Count("cheese", "e"))
	fmt.Println(bytes.Index([]byte("chicken"), []byte("ken")))
	fmt.Println(bytes.LastIndexByte([]byte("chicken"), 'e'))
	fmt.Println(bytes.Count([]byte("cheese"), []byte("e")))
}
