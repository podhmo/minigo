package main

import (
	"bytes"
	"encoding/json"

	"example.com/hostenc"
)

type P struct {
	Name string `json:"name"`
	Age  int    `json:"age,omitempty"`
}

// Encode hands a script encoder and script values to a bound host func
// that calls Encode itself — the host path must still marshal with the
// script struct's tags.
func Encode() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	if err := hostenc.Call(enc, P{Name: "hello"}); err != nil {
		return "err: " + err.Error()
	}
	if err := hostenc.Call(enc, []P{{Name: "a"}, {Name: "b", Age: 2}}); err != nil {
		return "err: " + err.Error()
	}
	return b.String()
}
