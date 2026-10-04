package sellib2

func Id[T any](x T) T { return x }

type Pair[A, B any] struct {
	a A
	b B
}
