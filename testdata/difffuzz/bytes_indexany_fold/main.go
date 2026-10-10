package main

import (
	"bytes"
	"fmt"
)

func main() {
	fmt.Println(bytes.IndexAny([]byte("select *"), "*<`"))
	fmt.Println(bytes.IndexAny([]byte("a' b"), "\"'`"))
	fmt.Println(bytes.IndexAny([]byte("none"), "xyz"))
	fmt.Println(bytes.EqualFold([]byte("AbC"), []byte("aBc")))
	fmt.Println(bytes.EqualFold([]byte("AbC"), []byte("ab")))
}
