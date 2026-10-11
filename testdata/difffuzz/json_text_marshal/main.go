package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

type Key int

func (k Key) MarshalText() ([]byte, error)  { return []byte(fmt.Sprintf("K:%d", int(k))), nil }
func (k *Key) UnmarshalText(b []byte) error { *k = Key(len(b) * 10); return nil }

type V struct{ N int }

func (v V) MarshalText() ([]byte, error)  { return []byte(fmt.Sprintf("v%d", v.N)), nil }
func (v *V) UnmarshalText(b []byte) error { v.N = len(b); return nil }

type St struct{ S string }

func (s St) MarshalText() ([]byte, error)  { return []byte("st:" + s.S), nil }
func (s *St) UnmarshalText(b []byte) error { s.S = "U:" + string(b); return nil }

type Both struct{ X int }

func (b Both) MarshalJSON() ([]byte, error) { return []byte(`"JSON"`), nil }
func (b Both) MarshalText() ([]byte, error) { return []byte("TEXT"), nil }

type UB struct{ S string }

func (u *UB) UnmarshalJSON(b []byte) error { u.S = "J:" + string(b); return nil }
func (u *UB) UnmarshalText(b []byte) error { u.S = "T:" + string(b); return nil }

type MErr struct{}

func (m MErr) MarshalText() ([]byte, error) { return nil, errors.New("mt-err") }

type Num int

func (n Num) MarshalText() ([]byte, error)  { return []byte(fmt.Sprintf("n%d", n)), nil }
func (n *Num) UnmarshalText(b []byte) error { *n = Num(len(b)); return nil }

type W struct {
	N Num `json:"n,string"`
}

type Outer struct {
	F V           `json:"f"`
	M map[Key]int `json:"m"`
}

func main() {
	b, _ := json.Marshal(map[Key]int{1: 2})
	fmt.Println(string(b))
	b, _ = json.Marshal(map[St]int{{"a"}: 1})
	fmt.Println(string(b))
	b, _ = json.Marshal(V{3})
	fmt.Println(string(b))
	b, _ = json.Marshal(Both{})
	fmt.Println(string(b))
	b, _ = json.Marshal(Outer{F: V{5}, M: map[Key]int{2: 3}})
	fmt.Println(string(b))

	var m map[Key]int
	err := json.Unmarshal([]byte(`{"K:7":9}`), &m)
	fmt.Println(m, err)
	var v V
	err = json.Unmarshal([]byte(`"abcd"`), &v)
	fmt.Println(v, err)
	var st St
	err = json.Unmarshal([]byte(`"hi"`), &st)
	fmt.Println(st, err)
	var m2 map[St]int
	err = json.Unmarshal([]byte(`{"zz":4}`), &m2)
	fmt.Println(m2, err)
	var o Outer
	err = json.Unmarshal([]byte(`{"f":"xx","m":{"K:3":4}}`), &o)
	fmt.Println(o, err)
	var u UB
	err = json.Unmarshal([]byte(`"zz"`), &u)
	fmt.Println(u, err)
	var um map[UB]int
	err = json.Unmarshal([]byte(`{"zz":1}`), &um)
	fmt.Println(um, err)
	var w W
	err = json.Unmarshal([]byte(`{"n":"abcde"}`), &w)
	fmt.Println(w, err)
	b, _ = json.Marshal(W{N: 3})
	fmt.Println(string(b))
	b, err = json.Marshal(MErr{})
	fmt.Println(string(b), err)
	b, err = json.Marshal(map[MErr]int{{}: 1})
	fmt.Println(string(b), err)
	err = json.Unmarshal([]byte(`12`), &v)
	fmt.Println(v, err)
	var pv *V
	err = json.Unmarshal([]byte(`"abcd"`), &pv)
	fmt.Println(pv, err)
}
