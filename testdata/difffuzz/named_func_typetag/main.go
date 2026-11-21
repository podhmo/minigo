package main

import (
	"bufio"
	"fmt"
)

type S2 func([]byte, bool) (int, []byte, error)

func main() {
	var sf bufio.SplitFunc = bufio.ScanLines
	fmt.Printf("%T\n", sf)
	var sf2 = bufio.ScanWords
	fmt.Printf("%T\n", sf2)
	var f func([]byte, bool) (int, []byte, error) = sf
	fmt.Printf("%T\n", f)
	var g S2 = f
	fmt.Printf("%T\n", g)
	var a func() = func() {}
	fmt.Printf("%T\n", a)
}
