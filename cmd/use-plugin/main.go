package main

import (
	"fmt"
	"plugin"
)

func main() {
	p, err := plugin.Open("plugin/strings.so")
	if err != nil {
		panic(err)
	}
	f, err := p.Lookup("ToUpper")
	if err != nil {
		panic(err)
	}

	fmt.Println(f.(func(string) string)("Hello, World!"))
}
