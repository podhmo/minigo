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
	p(map[string]float32{"x": 0.1})
	p(S{F: 0.1})
}
