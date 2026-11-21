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
	P T   `json:"p"`
	A any `json:"a"`
}

type E struct{ P T }
type Outer struct{ *E }
type VOuter struct{ E }

func main() {
	v := S{P: T{X: 6}, A: T{X: 5}}
	// an addressable parent's field sees (*T).MarshalText; an
	// interface-held value is a copy — never addressable
	b, _ := json.Marshal(&v)
	fmt.Println(string(b))
	// a value copy does not
	b, _ = json.Marshal(v)
	fmt.Println(string(b))
	// an array by value does not either
	b, _ = json.Marshal([1]T{{X: 7}})
	fmt.Println(string(b))
	// []any elements are copies inside the interface
	b, _ = json.Marshal([]any{T{X: 9}})
	fmt.Println(string(b))
	// a promoted field below a *E embed stays addressable even
	// when the outer struct marshals by value
	o := Outer{E: &E{P: T{X: 4}}}
	b, _ = json.Marshal(o)
	fmt.Println(string(b))
	// a value embed follows the outer struct's addressability
	vo := VOuter{E: E{P: T{X: 3}}}
	b, _ = json.Marshal(vo)
	fmt.Println(string(b))
	b, _ = json.Marshal(&vo)
	fmt.Println(string(b))
	// slice elements are always addressable
	b, _ = json.Marshal([]T{{X: 2}})
	fmt.Println(string(b))
	// map values never are
	b, _ = json.Marshal(map[string]T{"k": {X: 8}})
	fmt.Println(string(b))
}
