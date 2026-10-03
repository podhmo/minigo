package main

import "fmt"

func main() {
	// `s != <untyped rune const>` lifted s into the constant domain and
	// pushed a constant.Value (not a bool) — `if` read it as truthy so
	// both branches fired.
	s := 10180
	if s != 'a'+'b'+'c'+'d'+'☺' {
		fmt.Println("wrong: ne fired")
	} else {
		fmt.Println("ne ok")
	}
	if s == 'a'+'b'+'c'+'d'+'☺' {
		fmt.Println("eq ok")
	} else {
		fmt.Println("wrong: eq skipped")
	}
	u := 0
	if u != 1<<40 {
		fmt.Println("bigconst ne ok")
	}
	if s > 'a'+'b' {
		fmt.Println("gt ok")
	}
}
