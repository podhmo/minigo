package main

import (
	"fmt"
	"reflect"
	"text/template/parse"
)

func main() {
	var x reflect.Kind = 2
	var y reflect.Kind
	fmt.Printf("%T %v %T %v\n", x, x, y, y)
	fmt.Println(x == reflect.Int, y == reflect.Invalid)
	var n parse.NodeType = 0
	fmt.Println(n == parse.NodeText, parse.NodeText == n)
	m := parse.NodeType(1)
	fmt.Println(m == parse.NodeAction, m)
}
