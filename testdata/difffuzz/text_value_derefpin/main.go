package main

import "fmt"

func fp() (*int, int) { return nil, 42 }

func main() {
	// multi-assign pins deref operands: *p binds through the old p
	p := new(int)
	p, *p = fp()
	fmt.Println("p nil:", p == nil)

	// single-assign keeps the live deref: *p = f() writes through
	// the reseated pointer
	q := new(int)
	r := new(int)
	*q = 1
	*r = 7
	old := q
	*q = func() int { q = r; return 42 }()
	fmt.Println("q==r:", q == r, "*q:", *q, "*old:", *old)

	// multi-assign pins index bases: s, s[i] = f() writes old s
	s := []int{1, 2, 3}
	os := s
	s, s[1] = func() ([]int, int) { return []int{9, 9, 9}, 7 }()
	fmt.Println("s:", s, "os:", os)
}
