package dep1

// Word is a type here — but the use site's dep is dep2.
type Word int64

func F(x int) Word { return Word(x) }
