package main

import (
	"fmt"
	"strings"
)

func pair() (string, string) { return "abab", "a" }

func main() {
	fmt.Println("x", strings.Count(pair()))
}
