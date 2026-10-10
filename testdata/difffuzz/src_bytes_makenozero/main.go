package main

// --src bytes: bytes.Join and bytes.Repeat build their result through
// internal/bytealg.MakeNoZero, which must hand the caller a zeroed
// []byte of len n like the runtime helper it stands in for.

import (
	"bytes"
	"fmt"
)

func main() {
	fmt.Println(string(bytes.Join([][]byte{[]byte("a"), []byte("b"), []byte("c")}, []byte(", "))))
	fmt.Println(string(bytes.Repeat([]byte("xy"), 3)))
	fmt.Println(string(bytes.ToUpper([]byte("mix3d"))))
}
