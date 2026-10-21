// Package genericbench drives BenchmarkGenericCall: each entry point
// calls a trivial function 100k times, plain or generic.
package genericbench

const n = 100000

func Plain(x int) int { return x }

func Id[T any](x T) T { return x }

type Box[T any] struct{ v T }

func (b Box[T]) Get() T { return b.v }

func Pair[K comparable, V any](k K, v V) V { return v }

func RunPlain() int {
	s := 0
	for i := 0; i < n; i++ {
		s += Plain(i)
	}
	return s
}

func RunInferred() int {
	s := 0
	for i := 0; i < n; i++ {
		s += Id(i)
	}
	return s
}

func RunExplicit() int {
	s := 0
	for i := 0; i < n; i++ {
		s += Id[int](i)
	}
	return s
}

func RunPartial() int {
	s := 0
	for i := 0; i < n; i++ {
		s += Pair[string]("k", i)
	}
	return s
}

func RunMethod() int {
	s := 0
	b := Box[int]{v: 1}
	for i := 0; i < n; i++ {
		s += b.Get()
	}
	return s
}
