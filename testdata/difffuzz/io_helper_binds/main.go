package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func main() {
	var b bytes.Buffer
	rw := bufio.NewReadWriter(bufio.NewReader(strings.NewReader("r")), bufio.NewWriter(&b))
	fmt.Println(rw != nil)
	var out bytes.Buffer
	json.Indent(&out, []byte("{\"a\":1}"), "", "  ")
	fmt.Println(out.String())
	var out2 bytes.Buffer
	json.Compact(&out2, []byte("{ \"a\" : 1 }"))
	fmt.Println(out2.String())
}
