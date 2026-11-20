package main

import (
	"bytes"
	"encoding/json"
)

type Raw struct {
	Payload json.RawMessage `json:"payload"`
}

func main() string {
	// bound NewDecoder streams one value off the reader.
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"a":1} {"b":2}`)))
	var m1, m2 map[string]int
	if err := dec.Decode(&m1); err != nil {
		return "err1:" + err.Error()
	}
	if err := dec.Decode(&m2); err != nil {
		return "err2:" + err.Error()
	}
	// RawMessage keeps the literal bytes.
	var r Raw
	if err := json.Unmarshal([]byte(`{"payload":{"x":[1,2]}}`), &r); err != nil {
		return "err3:" + err.Error()
	}
	b, err := json.Marshal(r.Payload)
	if err != nil {
		return "err4:" + err.Error()
	}
	return string(rune('0'+m1["a"])) + string(rune('0'+m2["b"])) + "|" + string(b)
}
