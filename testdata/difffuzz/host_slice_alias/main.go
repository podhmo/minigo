package main

import (
	"bytes"
	"fmt"
	"net/http"
)

func main() {
	// buf.Bytes() shares the buffer's array — an element store reaches it
	var buf bytes.Buffer
	buf.WriteString("hello")
	bs := buf.Bytes()
	bs[0]++
	fmt.Println(string(buf.Bytes()))

	// a reslice keeps the same backing
	sub := bs[1:3]
	sub[0] = 'x'
	fmt.Println(string(buf.Bytes()))

	// copy writes through too
	copy(bs[1:], "yy")
	fmt.Println(string(buf.Bytes()))

	// a map element whose value is a slice shares the array
	h := http.Header{"A": []string{"x", "y"}}
	s := h["A"]
	s[0] = "v"
	fmt.Println(h["A"][0])

	// a range value's element store reaches the same backing
	for _, v := range h {
		v[1] = "w"
	}
	fmt.Println(h["A"])
}
