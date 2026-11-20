package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

func main() {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(map[string]int{"a": 1}); err != nil {
		fmt.Println("enc:", err)
		return
	}
	var m map[string]int
	if err := gob.NewDecoder(&buf).Decode(&m); err != nil {
		fmt.Println("dec:", err)
		return
	}
	fmt.Println(m)
}
