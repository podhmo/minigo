package inner

// Set is a generic interface; a value of it compares to nil lawfully.
type Set[T any] interface{ Has(T) bool }
