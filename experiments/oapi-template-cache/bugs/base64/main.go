package main

import (
	"encoding/json"
	"fmt"
)

type state struct{ Text []byte }

func main() {
	var decoded state
	if err := json.Unmarshal([]byte(`{"Text":"YQ=="}`), &decoded); err != nil {
		panic(err)
	}
	assigned := state{Text: decoded.Text}
	fmt.Println(string(assigned.Text))
}
