package main

import (
	"encoding/json"
	"fmt"
)

type T struct{ X int }

func (t *T) MarshalText() ([]byte, error) {
	return []byte(fmt.Sprintf("pt%d", t.X)), nil
}

type S struct {
	P T `json:"p"`
}

func main() {
	v := S{P: T{X: 6}}
	// an addressable parent's field sees (*T).MarshalText
	b, _ := json.Marshal(&v)
	fmt.Println(string(b))
	// a value copy does not
	b, _ = json.Marshal(v)
	fmt.Println(string(b))
	// slice elements are always addressable
	b, _ = json.Marshal([]T{{X: 7}})
	fmt.Println(string(b))
	// map values never are
	b, _ = json.Marshal(map[string]T{"k": {X: 8}})
	fmt.Println(string(b))
}
