package main

import "fmt"

type S string

func (s *S) Set(v string) { *s = S(v) }

func main() {
	var s S
	s.Set("asdf")
	fmt.Println(s)
}
