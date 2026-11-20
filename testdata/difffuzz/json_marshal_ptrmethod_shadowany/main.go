package main

import (
	"encoding/json"
	"fmt"
)

// a shadowed any is a concrete type: []U elements stay addressable
type any = int
type U any

func (u *U) MarshalText() ([]byte, error) { return []byte("pu"), nil }

func main() {
	b, _ := json.Marshal([]U{1})
	fmt.Println(string(b))
}
