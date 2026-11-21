package main

import (
	"encoding/json"
	"fmt"
)

func main() {
	// gc: string converts to the []byte-underlying named type and
	// marshals as a raw literal.
	r := json.RawMessage("{\"k\":1}")
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}
