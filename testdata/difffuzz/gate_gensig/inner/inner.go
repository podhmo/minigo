package inner

type T int

func F[T any](v T) T { return v }
