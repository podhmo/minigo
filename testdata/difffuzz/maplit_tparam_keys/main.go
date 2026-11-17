package main

import "fmt"

const k = "kk"

type S struct{ F int }

func getM[M ~map[string]int](m M) M {
	x := M{k: 1}
	y := []M{{k: 2}}
	x["k"] = 0
	return M{"a": y[0][k], "b": x[k]}
}

func getS[S ~struct{ F int }](s S) S {
	return S{F: 7}
}

func main() {
	fmt.Println(getM(map[string]int{}))
	fmt.Println(getS(S{}))
}
