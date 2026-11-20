package main

import (
	"fmt"
	"iter"
)

func main() {
	next, stop := iter.Pull(func(yield func(int) bool) {
		yield(1)
		yield(2)
	})
	defer stop()
	for {
		v, ok := next()
		if !ok {
			break
		}
		fmt.Println(v)
	}
}
