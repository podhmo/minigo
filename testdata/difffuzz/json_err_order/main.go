package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

type Fail string

func (f *Fail) UnmarshalJSON(b []byte) error { return errors.New("custom fail") }

// gc reports the FIRST error in input order — a script UnmarshalJSON
// error competes at its member's position instead of preempting.
func main() {
	var s struct {
		A int
		B Fail
	}
	fmt.Println(json.Unmarshal([]byte(`{"a":"notint","b":"x"}`), &s))
	fmt.Println(json.Unmarshal([]byte(`{"b":"x","a":"notint"}`), &s))
}
