package main

import "fmt"

type big float64

func main() {
	var f float64 = 16717361816799281152
	fmt.Println(f)
	var g float64 = 9223372036854775808
	fmt.Println(g)
	u := uint64(f)
	fmt.Println(big(u))
}
