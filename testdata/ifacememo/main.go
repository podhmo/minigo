package main

type G struct{ V int }

type H struct{ V int }

func (h H) M() int { return h.V }

type HasM interface{ M() int }

func MakeG() any { return G{V: 1} }

func IsHasM(x any) bool {
	_, ok := x.(HasM)
	return ok
}

func main() {}
