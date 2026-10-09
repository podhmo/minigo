package main

import (
	"fmt"
	"sort"
	"strings"
)

func main() {
	func() {
		defer func() { fmt.Println("recover", recover()) }()
		strings.Map(func(r rune) rune { panic("map") }, "a")
	}()
	func() {
		defer func() { fmt.Println("recover", recover()) }()
		sort.Search(2, func(i int) bool { panic("sort") })
	}()
}
