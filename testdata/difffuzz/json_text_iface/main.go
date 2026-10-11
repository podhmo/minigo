package main

import (
	"encoding"
	"fmt"
)

type S string

func (s S) MarshalText() ([]byte, error)  { return []byte("t:" + string(s)), nil }
func (s *S) UnmarshalText(b []byte) error { *s = S("u:" + string(b)); return nil }

func main() {
	var tm encoding.TextMarshaler = S("x")
	b, _ := tm.MarshalText()
	fmt.Println(string(b))
	var tu encoding.TextUnmarshaler = new(S)
	_ = tu.UnmarshalText([]byte("y"))
	fmt.Println(*(tu.(*S)))
}
