package main

import (
	"fmt"
	"time"
)

func main() {
	var x any = 2 * time.Second
	_, ok := x.(fmt.Stringer)
	fmt.Println(ok)
	m := map[fmt.Stringer]int{}
	var d time.Duration = 3 * time.Second
	m[d] = 1
	fmt.Println(m[d])
}
