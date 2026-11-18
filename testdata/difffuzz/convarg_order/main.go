package main

import (
	"fmt"
	"time"
)

var x = 1

func bump() int { x = 2; return 2 }

func take(d time.Duration, i int) int { return int(d) + i }

func main() {
	fmt.Println(take(time.Duration(x), bump()))
}
