package main

import "fmt"

type Point struct{ X, Y int }

func try(f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic:", r)
		}
	}()
	f()
}

func main() {
	try(func() {
		var a any = &Point{5, 6}
		_ = a.(int)
	})
	try(func() {
		var a any = []int{1}
		_ = a.(int)
	})
}
