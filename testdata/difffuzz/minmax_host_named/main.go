package main

import (
	"fmt"
	"time"
)

func main() {
	d := 3 * time.Second
	fmt.Println(max(d, 5000000000))
	fmt.Println(min(d, 5000000000))
	fmt.Printf("%T\n", max(d, 5000000000))
	fmt.Printf("%T\n", min(d, 5000000000))
}
