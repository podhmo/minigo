package main

import (
	"encoding/json"
	"fmt"
)

type Money string

func (m Money) MarshalJSON() ([]byte, error)  { return []byte(fmt.Sprintf("%q", "M:"+string(m))), nil }
func (m *Money) UnmarshalJSON(b []byte) error { *m = Money("U:" + string(b)); return nil }

type Euro string

func (e *Euro) UnmarshalJSON(b []byte) error { *e = Euro("E:" + string(b)); return nil }

type HasEmbed struct{ Money }

type HasPtrEmbed struct{ *Money }

type HasTwo struct {
	Money
	Euro
}

type WithField struct {
	Money
	Other string
}

type Outer struct {
	Name string
	Money
	Tag int `json:"tag"`
}

func main() {
	b, err := json.Marshal(HasEmbed{Money: "x"})
	fmt.Println("m1:", string(b), err)
	var h HasEmbed
	err = json.Unmarshal([]byte(`{"Money":"10"}`), &h)
	fmt.Println("u1:", h.Money, err)
	var h2 HasEmbed
	err = json.Unmarshal([]byte(`{"a":1}`), &h2)
	fmt.Println("u2:", h2.Money, err)
	var o Outer
	err = json.Unmarshal([]byte(`{"Name":"n","tag":3,"z":true}`), &o)
	fmt.Println("u3:", o.Name, o.Money, o.Tag, err)
	bo, err := json.Marshal(Outer{Name: "n", Money: "m", Tag: 3})
	fmt.Println("m3:", string(bo), err)
	p2 := HasPtrEmbed{Money: new(Money)}
	err = json.Unmarshal([]byte(`{"Money":"10"}`), &p2)
	fmt.Println("p2:", *p2.Money, err)
	var t HasTwo
	err = json.Unmarshal([]byte(`{"Money":"10"}`), &t)
	fmt.Println("t1:", t.Money, t.Euro, err)
	var w WithField
	err = json.Unmarshal([]byte(`{"Money":"10","Other":"o"}`), &w)
	fmt.Println("w1:", w.Money, w.Other, err)
	b, err = json.Marshal(WithField{Money: "m", Other: "o"})
	fmt.Println("wm:", string(b), err)
}
