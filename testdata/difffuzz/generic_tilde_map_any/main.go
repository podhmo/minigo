package main

// `M ~map[K]V` with V=any accepts `type FuncMap map[string]any` —
// text/template.Clone calls maps.Copy on its FuncMaps.

import "fmt"

type FuncMap map[string]any

type Ints map[string]int

func Copy[M1 ~map[K]V, M2 ~map[K]V, K comparable, V any](dst M1, src M2) {
	for k, v := range src {
		dst[k] = v
	}
}

func main() {
	a, b := FuncMap{}, FuncMap{"x": 1}
	Copy(a, b)
	c := Ints{}
	Copy(c, map[string]int{"y": 2})
	fmt.Println(len(a), c["y"])
}
