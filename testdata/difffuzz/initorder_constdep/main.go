package main

// Constants initialize before variables in gc's initOrder regardless of
// dependency counts (go.dev/issue/66575): a var whose initializer reaches
// a const still keeps its declaration-order rank against later vars.

import "fmt"

var (
	v0 = initv0()
	v1 = initv1()
)

const c = "c"

func initv0() string {
	fmt.Println("initv0")
	if c != "" {
		return ""
	}
	return ""
}

func initv1() string {
	fmt.Println("initv1")
	return ""
}

func main() {
	fmt.Println(len(v0)+len(v1) == 0)
}
