package main

import "fmt"

type Point struct{ X, Y int }

func (p *Point) Scale(k int) { p.X *= k; p.Y *= k }

func main() {
	s := []Point{{1, 2}, {0, 0}}
	for i := range s {
		s[i].Scale(2)
	}
	fmt.Println(s)
}
