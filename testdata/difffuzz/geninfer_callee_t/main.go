package main

import "fmt"

type Foo interface {
	~float64
}

func bar[T Foo](x T, y func(a T) T) T {
	return y(x)
}

func f[T Foo](x T) T {
	return x
}

func g[T Foo](x T) T {
	return bar(0, f[T])
}

func main() {
	fmt.Println(g(0.0))
}
