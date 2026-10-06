package main

import "fmt"

type Point struct{ X, Y int }

func (p Point) Add(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }

func try(f func()) (r any) {
	defer func() { r = recover() }()
	f()
	return nil
}

func main() {
	var pp *Point
	fmt.Println(try(func() { fmt.Println((*pp).X) }))
	fmt.Println(try(func() { fmt.Println((*pp).Add(Point{1, 2})) }))
}
