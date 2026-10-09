package main

import (
	"fmt"
	"text/template/parse"
)

func main() {
	fmt.Println(parse.NodeText+1 == parse.NodeAction)
	fmt.Println(int(parse.NodeAction))
	fmt.Println(parse.ParseComments == 1)
}
