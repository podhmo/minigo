package main

import (
	"fmt"
	"time"
)

func main() {
	var loc time.Location
	fmt.Printf("%q %v\n", loc.String(), &loc == time.Local)
	n := new(time.Location)
	fmt.Printf("%q\n", n.String())
	t := time.Unix(0, 0).In(&loc)
	fmt.Println(t.Year())
}
