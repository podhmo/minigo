package main

import (
	"encoding/json"
	"fmt"
)

type T struct {
	B []byte
}
type Bs []byte
type T2 struct {
	B Bs
}

func main() {
	var d T
	if err := json.Unmarshal([]byte(`{"B":"AQI="}`), &d); err != nil {
		fmt.Println("unmarshal err:", err)
		return
	}
	t2 := T{B: d.B}
	fmt.Println("roundtrip:", t2.B)

	var s []byte = d.B
	fmt.Println("var:", s)
	fmt.Println("conv:", []byte(d.B))
	fmt.Println("append:", append(d.B, 3))

	var d2 T2
	if err := json.Unmarshal([]byte(`{"B":"AQI="}`), &d2); err != nil {
		fmt.Println("unmarshal2 err:", err)
		return
	}
	fmt.Println("named:", d2.B, len(d2.B))
	var b Bs = d2.B
	fmt.Println("named var:", b)

	var d3 T
	if err := json.Unmarshal([]byte(`{"B":[3,4]}`), &d3); err != nil {
		fmt.Println("unmarshal3 err:", err)
		return
	}
	fmt.Println("array:", d3.B)
	var d4 T
	if err := json.Unmarshal([]byte(`{"B":null}`), &d4); err != nil {
		fmt.Println("unmarshal4 err:", err)
		return
	}
	fmt.Println("null:", d4.B == nil)
}
