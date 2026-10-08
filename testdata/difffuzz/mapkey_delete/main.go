package main

import "fmt"

type A struct{ X int }

func main() {
	// delete coerces the key operand to the declared key type like an
	// index expression does — the stored float64(1) key is removed by
	// delete(m, 1), not missed as int64(1).
	m := map[float64]int{1: 7}
	delete(m, 1)
	fmt.Println(len(m))
	m2 := map[A]int{A{1}: 5}
	delete(m2, A{1})
	fmt.Println(len(m2))
	m3 := map[string]int{"k": 3}
	delete(m3, "k")
	fmt.Println(len(m3))
	var nm map[int]int
	delete(nm, 2)
	fmt.Println("nil-ok")
}
