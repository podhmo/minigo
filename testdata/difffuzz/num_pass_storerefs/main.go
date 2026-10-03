package main

import "fmt"

type S struct{ X int }

func replace(s *S) int {
	*s = S{X: 1}
	return 2
}

func main() {
	// a store target resolves the variable's live storage, so a RHS
	// that replaces the whole variable still lands the write there
	s := S{}
	s.X = replace(&s)
	fmt.Println(s.X) // 2

	arr := []int{1, 2}
	arr[1] += func() int { arr = []int{9, 9}; return 3 }()
	fmt.Println(arr[1]) // 12: read after the RHS ran, stored to the new slice

	p, q := &S{X: 10}, &S{X: 0}
	(*p).X += func() int { p = q; return 4 }()
	fmt.Println(q.X) // 4: follows the reseated pointer

	i := 10
	i += func() int { i = 1; return 5 }()
	fmt.Println(i) // 6
}
