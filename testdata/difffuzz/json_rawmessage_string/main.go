package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
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

	// decoding into an interface cell replaces the content (gc's
	// indirect only descends into interface-held pointers).
	var a any = time.Time{}
	if err := json.Unmarshal([]byte("\"hello\""), &a); err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(a)
	}
	t := time.Time{}
	var p any = &t
	if err := json.Unmarshal([]byte("\"2020-01-02T03:04:05Z\""), &p); err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(t.Format("2006"))
	}
	x := 1
	var q any = &x
	if err := json.Unmarshal([]byte("5"), &q); err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(x)
	}
	if err := json.Unmarshal([]byte("null"), &q); err != nil {
		fmt.Println(err)
	} else {
		fmt.Println(q)
	}
}
