package main

import (
	"fmt"
	"text/template/parse"
)

func main() {
	t := parse.New("x", map[string]any{"f": func() string { return "ok" }})
	fmt.Println(t.Name)
}
