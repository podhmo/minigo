package main

import (
	"crypto/md5"
	"fmt"
	"io"
)

func main() {
	fmt.Printf("%x\n", md5.Sum([]byte("hello")))
	h := md5.New()
	io.WriteString(h, "hello world")
	fmt.Printf("%x\n", h.Sum(nil))
	h.Reset()
	h.Write([]byte("a"))
	h.Write([]byte("bc"))
	fmt.Printf("%x\n", h.Sum(nil))
}
