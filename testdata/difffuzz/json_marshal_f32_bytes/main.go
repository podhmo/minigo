package main

import (
	"encoding/json"
	"fmt"
)

type F32 float32
type B []byte
type MyByte byte

type S struct{ F float32 }

func main() {
	p := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	p(float32(0.1)) // gc encodes at 32-bit precision
	p(F32(0.1))     // named float32 keeps the tag's width
	p([]float32{0.1})
	p([]byte{104, 105})      // base64, not element-wise
	p(B{104, 105})           // named []byte still base64
	p([]MyByte{104, 105})    // named byte element still base64
	p([3]byte{104, 105, 33}) // an array stays element-wise
	p([]MJByte{104, 105})    // element marshaler demotes base64
	p([]SigByte{104, 105})   // wrong MarshalJSON sig: base64 stays
	p([]AlByte{104, 105})    // alias results still marshal per element
	p(map[string]float32{"x": 0.1})
	p(S{F: 0.1})
}

type MJByte byte

func (m MJByte) MarshalJSON() ([]byte, error) { return []byte(`"X!"`), nil }

type SigByte byte

// MarshalJSON() string does not satisfy json.Marshaler — gc keeps
// the base64 encoding for []SigByte.
func (s SigByte) MarshalJSON() string { return "x" }

type AliasBytes = []byte
type AliasErr = error

type AlByte byte

// Alias results satisfy the marshaler signature identically (gc:
// type X = []byte IS []byte), so []AlByte marshals per element.
func (a AlByte) MarshalJSON() (AliasBytes, AliasErr) { return []byte(`"A!"`), nil }
