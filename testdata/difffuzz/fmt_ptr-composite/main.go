package main

import (
	"fmt"
	"strings"
)

type S struct{ A int }
type NS []int

func hex(x any) bool { return strings.HasPrefix(fmt.Sprint(x), "0x") }

func main() {
	var ns []int
	var nm map[string]int
	var nst S
	var np *int
	st := &S{A: 3}
	a := [4]byte{1, 2, 3, 4}
	s := []int{1, 2}
	n := NS{7, 8}
	m := map[string]int{"k": 1}
	x := 5
	px := &x
	pp := &px
	pns := &ns
	pnm := &nm
	pst := &nst
	pnp := &np
	fmt.Println(&a, &s, &n, &m)
	fmt.Println(hex(px), hex(pp), hex(&px))
	fmt.Println(pns, pnm, pst, hex(pnp))
	// inside composites pointers keep the address — assert shape only
	fmt.Println(strings.HasPrefix(fmt.Sprint([]*S{st}), "[0x"), strings.HasSuffix(fmt.Sprint([]*S{st}), "]"))
	fmt.Println(strings.HasPrefix(fmt.Sprint(map[string]*S{"a": st}), "map[a:0x"))
	fmt.Println(strings.HasPrefix(fmt.Sprint(struct{ P *S }{P: st}), "{0x"))
	fmt.Println([][]int{{1}})
	fmt.Printf("%#v\n", st)
	fmt.Println(strings.HasPrefix(fmt.Sprintf("%#v", []*S{st}), "[]*main.S{(*main.S)(0x"))
	fmt.Printf("%#v\n", pns)
	fmt.Println(strings.HasPrefix(fmt.Sprintf("%+v", []*S{st}), "[0x"))
	var anys []any
	anys = append(anys, st)
	fmt.Println(strings.HasPrefix(fmt.Sprint(anys), "[0x"))
	type Outer struct{ Inner *S }
	fmt.Println(strings.HasPrefix(fmt.Sprint(&Outer{Inner: st}), "&{0x"))
}
