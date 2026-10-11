package main

import (
	"encoding/json"
	"fmt"
)

type U string

func (u *U) UnmarshalJSON(b []byte) error {
	*u = U("w")
	return nil
}

// an interface-typed target routes into UnmarshalJSON like gc: the
// Marshaler/Unmarshaler interface names resolve and the member probe
// fires on the held *U.
func main() {
	var i json.Unmarshaler = new(U)
	err := json.Unmarshal([]byte(`"q"`), &i)
	fmt.Println(*i.(*U), err)
}
