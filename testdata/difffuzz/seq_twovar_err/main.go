package main

import (
	"fmt"
	"strings"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("recovered")
		}
	}()
	for i, s := range strings.SplitSeq("a,b,c", ",") {
		fmt.Println(i, s)
	}
	fmt.Println("done")
}
