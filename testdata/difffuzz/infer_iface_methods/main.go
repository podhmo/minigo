package main

import "fmt"

type Getter[K comparable, V any] interface{ Get(K) V }

type M struct{}

func (M) Get(k string) int { return 1 }

func F[K comparable, V any](g Getter[K, V]) V { return g.Get(*new(K)) }

func main() {
	fmt.Println(F(M{}))
}
