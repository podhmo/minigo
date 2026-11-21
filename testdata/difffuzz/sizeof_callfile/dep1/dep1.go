package dep1

type Inner struct{ A, B int64 }

func New() Inner { return Inner{A: 1, B: 2} }
