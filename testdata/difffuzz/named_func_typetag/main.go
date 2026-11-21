package main

import (
	"bufio"
	"fmt"
)

func main() {
	var sf bufio.SplitFunc = bufio.ScanLines
	fmt.Printf("%T\n", sf)
	var sf2 = bufio.ScanWords
	fmt.Printf("%T\n", sf2)
}
