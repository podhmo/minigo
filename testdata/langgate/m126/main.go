package main

type List[E any] []E

func (l List[E]) Reduce[R any](init R, f func(R, E) R) R { return init }

func main() {}
