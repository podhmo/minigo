package generic_alias

// G is a generic type.
type G[T any] struct{}

// T is a recursive generic alias.
// Parsing this should not cause an infinite loop.
type T G[T]
