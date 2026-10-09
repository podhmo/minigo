package main

import (
	"crypto/sha512"
	"fmt"
)

func main() {
	fmt.Printf("%x\n", sha512.Sum512([]byte("hello")))
}
