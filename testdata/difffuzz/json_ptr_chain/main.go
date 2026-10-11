package main

import (
	"encoding/json"
	"fmt"
)

type U struct{ S string }

func (u *U) UnmarshalJSON(b []byte) error {
	u.S = string(b) + "!"
	return nil
}

// gc's indirect() allocates the whole pointer chain, so a **U target
// hands UnmarshalJSON a fresh *U — the bound decoder allocated only
// the outer cell and the method derefs nil.
func main() {
	var p **U
	err := json.Unmarshal([]byte(`"q"`), &p)
	fmt.Println(*p, err)
}
