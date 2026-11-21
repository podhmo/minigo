package main

import "fmt"

type Graph[T any] map[string]Graph[T]

func Count[T any](g Graph[T]) int { return len(g) }

func main() {
	fmt.Println(Count(Graph[int]{"a": nil}))
}
