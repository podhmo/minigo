package main

import (
	"fmt"
	"net/http"
	"net/url"
)

const k = "kk"

type Pair[K comparable, V any] map[K]V

type Ms []map[string]int

type Box[T any] []T

type S[T any] []map[string]T

type PM map[string]*map[string]int

type AM = map[string]int

type AS = []map[string]int

type NM AS

type St struct{ k int }

type G[T any] struct{ k T }

type MK map[St]int

type Hs []http.Header

func main() {
	// map keys under a generic map, a named slice, `*T` elision
	fmt.Println(Pair[string, int]{k: 1})
	fmt.Println(Ms{{k: 1}})
	fmt.Println(*[]*map[string]int{{k: 1}}[0])
	fmt.Println(Box[map[string]int]{{k: 2}})
	fmt.Println(S[int]{{k: 3}})
	fmt.Println(*PM{"a": {k: 4}}["a"])
	fmt.Println([]AM{{k: 5}}, AS{{k: 6}}, NM{{k: 7}})
	fmt.Println([]url.Values{{k: {"v"}}})
	fmt.Println(map[string]http.Header{"a": {k: {"x"}}})
	fmt.Println(Hs{{k: {"y"}}})
	fmt.Println(map[string]Pair[string, bool]{"p": {k: true}})

	// local named types
	type L []map[string]int
	type LP []*map[string]int
	fmt.Println(L{{k: 8}}, *LP{{k: 9}}[0])
	type N2 Ms
	type B2 = Box[map[string]int]
	fmt.Println(N2{{k: 10}}, B2{{k: 11}})

	// nested literals below an elided `&T`, generic value types
	fmt.Println(*[]*[]map[string]int{{{k: 12}}}[0])
	fmt.Println(Pair[string, []map[string]int]{k: {{k: 13}}})

	// struct keys stay field names
	fmt.Println(St{k: 1}, []St{{k: 2}}, *[]*St{{k: 3}}[0])
	fmt.Println(map[string]St{"a": {k: 4}}, G[int]{k: 5}, []G[string]{{k: "s"}})
	fmt.Println(MK{{k: 6}: 1}, map[St]int{{k: 7}: 2})
	type LS struct{ k string }
	fmt.Println([]LS{{k: "l"}}, Box[St]{{k: 8}})
}
