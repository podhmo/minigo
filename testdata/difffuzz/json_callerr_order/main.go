package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

type A struct{}

func (v *A) UnmarshalJSON(bs []byte) error { return errors.New("AERR") }

type B struct{}

func (v *B) UnmarshalJSON(bs []byte) error { return errors.New("BERR") }

type S struct {
	X *A
	Y *B
}

func main() {
	var s S
	err := json.Unmarshal([]byte(`{"x":1,"y":2}`), &s)
	fmt.Println(err)
}
