package main

import (
	"fmt"
	"strconv"
)

// strconv.IntSize was missing from the bound strconv, trapping
// `undefined: strconv.IntSize`; interpreted encoding/base64 also reaches
// it through byte-slice decoding.
func main() {
	fmt.Println(strconv.IntSize)
	fmt.Println(strconv.IntSize / 8)
	fmt.Println(strconv.IntSize == 64)
	var buf [strconv.IntSize]byte
	fmt.Println(len(buf))
}
