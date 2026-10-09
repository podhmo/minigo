package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

type Money struct {
	Cents int64
	raw   string
}

func (m *Money) UnmarshalJSON(b []byte) error {
	m.raw = string(b)
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	m.Cents = n * 100
	return nil
}

func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%d", m.Cents/100)), nil
}

type Bad struct{}

func (b Bad) MarshalJSON() ([]byte, error) {
	return nil, errors.New("bad marshal")
}

type Wrap struct {
	Tag  string `json:"tag"`
	Cost *Money `json:"cost"`
}

func main() {
	m := Money{Cents: 12345}
	b, err := json.Marshal(m)
	fmt.Println(string(b), err)

	var m2 Money
	err = json.Unmarshal([]byte("999"), &m2)
	fmt.Println(m2.Cents, m2.raw, err)

	w := Wrap{Tag: "x", Cost: &Money{Cents: 500}}
	b2, _ := json.Marshal(w)
	fmt.Println(string(b2))

	var w2 Wrap
	err = json.Unmarshal([]byte(`{"tag":"y","cost":42}`), &w2)
	fmt.Println(w2.Tag, w2.Cost.Cents, w2.Cost.raw, err)

	var n *Money
	b3, _ := json.Marshal(n)
	fmt.Println(string(b3))

	b4, err4 := json.Marshal(Bad{})
	fmt.Println(string(b4), err4)

	var m3 Money
	err = json.Unmarshal([]byte(`"oops"`), &m3)
	fmt.Println(m3.Cents, err)
	nested()
}

type Upper string

func (u *Upper) UnmarshalJSON(b []byte) error {
	*u = Upper(string(b) + "!")
	return nil
}

type Holder struct {
	V string `json:"v"`
	N Upper  `json:"n"`
}

func nested() {
	var w Holder
	err := json.Unmarshal([]byte(`{"v":"y","n":3}`), &w)
	fmt.Printf("%s %s %v\n", w.V, w.N, err)

	var s []Upper
	err = json.Unmarshal([]byte(`["a","b"]`), &s)
	fmt.Println(s, err)

	var m map[string]Upper
	err = json.Unmarshal([]byte(`{"k":"z"}`), &m)
	fmt.Println(m, err)

	var p *Upper
	err = json.Unmarshal([]byte(`"q"`), &p)
	fmt.Println(*p, err)
}
