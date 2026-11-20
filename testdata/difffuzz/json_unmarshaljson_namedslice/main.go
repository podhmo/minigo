package main

import (
	"encoding/json"
	"fmt"
)

type Types []string

func (t *Types) Slice() []string { return *t }

func (t *Types) UnmarshalJSON(data []byte) error {
	var strings []string
	if err := json.Unmarshal(data, &strings); err != nil {
		return err
	}
	*t = strings
	return nil
}

type S struct {
	Type Types `json:"type"`
}

func main() {
	var s S
	if err := json.Unmarshal([]byte(`{"type":["string"]}`), &s); err != nil {
		panic(err)
	}
	// the method's `*t = strings` must keep the named slice's tag —
	// losing it makes s.Type a bare []string and .Slice() uncallable.
	fmt.Println(s.Type.Slice()[0])
}
