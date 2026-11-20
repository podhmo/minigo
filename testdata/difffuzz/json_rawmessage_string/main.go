package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func main() {
	// gc: string converts to the []byte-underlying named type and
	// marshals as a raw literal.
	r := json.RawMessage("{\"k\":1}")
	b, _ := json.Marshal(r)
	fmt.Println(string(b))

	// a converted RawMessage reads as Unmarshal's []byte source and
	// updates through &r's pointer UnmarshalJSON.
	var v map[string]int
	if err := json.Unmarshal(r, &v); err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(v)
	}
	if err := json.Unmarshal([]byte("{\"z\":2}"), &r); err != nil {
		fmt.Println(err)
	} else {
		b, _ := json.Marshal(r)
		fmt.Println(string(b))
	}

	// a conversion from a host-backed slice shares the backing: a
	// write through the []byte alias shows in the marshal output.
	buf := bytes.NewBufferString("123")
	bs := buf.Bytes()
	rm := json.RawMessage(bs)
	bs[0] = '7'
	b, _ = json.Marshal(rm)
	fmt.Println(string(b))
}
