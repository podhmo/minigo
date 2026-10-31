package main

import (
	"encoding/json"
	"fmt"
	"os"
)

type node struct{ x int }

type inner struct {
	Pub  int
	priv int
}

type myint int

type Inner2 struct{ Z int }

type A struct {
	Name   string `json:"name"`
	tree   *node
	body   string
	a, b   int
	Tagged string `json:"t,omitempty"`
}

type B struct {
	inner
	myint
	X int
}

type C struct {
	*inner
	Y int
}

type D struct {
	inner `json:"in"`
	W     int
}

type E struct {
	hidden string `json:"hidden"`
	Shown  string
}

type F struct {
	Inner2
	inner
}

func show(label string, v any) {
	b, err := json.Marshal(v)
	fmt.Printf("%s: %s %v\n", label, b, err)
}

func main() {
	show("A", A{Name: "n", body: "secret", a: 1})
	show("B", B{inner: inner{Pub: 1, priv: 2}, myint: 3, X: 4})
	show("C", C{inner: &inner{Pub: 5}, Y: 6})
	show("Cnil", C{Y: 6})
	show("D", D{inner: inner{Pub: 7}, W: 8})
	show("E", E{hidden: "h", Shown: "s"})
	show("F", F{Inner2{1}, inner{Pub: 2}})
	show("slice", []A{{Name: "x", body: "y"}})
	show("map", map[string]any{"a": A{Name: "z", body: "q"}})
	json.NewEncoder(os.Stdout).Encode(A{Name: "enc", body: "secret"})
	var e E
	err := json.Unmarshal([]byte(`{"hidden":"H","Shown":"S"}`), &e)
	fmt.Printf("unmarshal: %q %q %v\n", e.hidden, e.Shown, err)
	var a A
	err = json.Unmarshal([]byte(`{"name":"N","body":"B","a":9}`), &a)
	fmt.Printf("unmarshalA: %q %q %d %v\n", a.Name, a.body, a.a, err)
}
