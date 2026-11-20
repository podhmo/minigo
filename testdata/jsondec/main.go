package main

import (
	"bytes"
	"encoding/json"
)

type Raw struct {
	Payload json.RawMessage `json:"payload"`
}

type T struct {
	X int `json:"x"`
}

func (t T) Tag() string { return "t" }

type TBis T

func (b TBis) Tag() string { return "bis" }

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
	// DisallowUnknownFields rejects keys no field claims while the
	// known fields still land.
	strict := json.NewDecoder(bytes.NewReader([]byte(`{"a":1,"zzz":2,"b":"x"}`)))
	strict.DisallowUnknownFields()
	var s struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	serr := strict.Decode(&s)
	if serr == nil {
		return "err5:no error"
	}
	// int64 literals keep their low bits past 2^53.
	var big int64
	if err := json.Unmarshal([]byte(`9007199254740993`), &big); err != nil {
		return "err6:" + err.Error()
	}
	// a named-delegated decode keeps the declared type's methods.
	var tb TBis
	if err := json.Unmarshal([]byte(`{"x":1}`), &tb); err != nil {
		return "err7:" + err.Error()
	}
	return string(rune('0'+m1["a"])) + string(rune('0'+m2["b"])) + "|" + string(b) +
		"|" + serr.Error() + "|" + itoa(big) + "|" + tb.Tag()
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	var buf [24]byte
	p := len(buf)
	neg := i < 0
	for i != 0 {
		p--
		d := i % 10
		if neg {
			d = -d
		}
		buf[p] = byte('0' + d)
		i /= 10
	}
	if neg {
		p--
		buf[p] = '-'
	}
	return string(buf[p:])
}
