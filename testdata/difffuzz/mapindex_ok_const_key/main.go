package main

import "fmt"

const keyA = "__origin__"

func look(val map[string]any) {
	_, ok := val[keyA]
	_, okB := val[keyB]
	k := keyA
	_, okC := val[k]
	v := val[keyA]
	fmt.Println(ok, okB, okC, v)
	delete(val, keyB)
	fmt.Println(len(val))
}

const keyB = "__origin__"

const one = 1

type Key string

const kk Key = "k"

func main() {
	look(map[string]any{"__origin__": 1, "x": 2})
	ma := map[any]string{1: "int", 1.5: "float"}
	_, okInt := ma[one]
	mf := map[float64]string{1: "f"}
	_, okF := mf[one]
	mk := map[Key]int{"k": 3}
	n, okK := mk[kk]
	fmt.Println(okInt, okF, n, okK)
}
