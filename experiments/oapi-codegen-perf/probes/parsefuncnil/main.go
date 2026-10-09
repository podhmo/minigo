package main

import (
	"fmt"
	"text/template/parse"
)

func main() {
	var f func()
	_, err := parse.Parse("x", "{{f}}", "", "", map[string]any{"f": f})
	fmt.Println(err)
}
