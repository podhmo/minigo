package main

import (
	"fmt"
	"time"
)

func main() {
	var d time.Duration = 3 * time.Second
	fmt.Println(d.String())
}
