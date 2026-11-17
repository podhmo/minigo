package main

import "fmt"

// a local `type rune int` shadows the predeclared name, but a char
// literal's default type is still the predeclared rune (int32): gc
// accepts `case int32(97)` under it.
type rune int

func main() {
	switch 'a' {
	case int32(97):
		fmt.Println("hit")
	}
}
