// A pointer method called through an addressable nil slice field
// (implicit &p.errors) must get a pointer to that field, as
// go/scanner.ErrorList.Add does inside go/parser.
package main

import "fmt"

type List []int

func (l *List) Add(x int) { *l = append(*l, x) }

type parser struct {
	errors List
}

func main() {
	p := &parser{}
	p.errors.Add(1)
	p.errors.Add(2)
	fmt.Println(len(p.errors), p.errors)
}
