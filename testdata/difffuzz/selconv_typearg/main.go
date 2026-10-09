package main

import (
	"fmt"
	"time"
)

type MyDur int64

func main() {
	var s MyDur = 5
	fmt.Println(time.Duration(5) == s)
}
