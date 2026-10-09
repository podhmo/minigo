package main

import "fmt"

// ++/-- through an index or field of an element writes the element's
// own storage, not a copy of the index read — `s[i].f++` must land in
// s[i]. The compiler evaluates the operand as a storage ref (the same
// ref/deref/add/store shape as `x op= y`), so named-integer indexes,
// arrays, nested selects and pointer elements all write through; a
// nil base panics like a plain select would.

type I int
type S struct{ N int }

func main() {
	s := []S{{N: 1}}
	s[0].N++
	var i I
	s[i].N++
	fmt.Println(s[0].N)

	var a [1]S
	a[0].N++
	fmt.Println(a[0].N)

	m := map[I][]int{0: {1}}
	m[i][0]++
	fmt.Println(m[0][0])

	// pointer elements write through the pointer, like `m[k].f = v`
	mp := map[int]*S{0: {N: 5}}
	mp[0].N++
	fmt.Println(mp[0].N)

	// nested select through the element
	type Outer struct{ In S }
	o := []Outer{{In: S{N: 7}}}
	o[0].In.N++
	fmt.Println(o[0].In.N)

	// scalar targets keep working
	var p *int
	func() {
		defer func() { fmt.Println("starnil", recover() != nil) }()
		*p++
	}()
	q := []int{1}
	q[0]++
	fmt.Println(q[0])
	x := S{N: 9}
	px := &x
	(*px).N++
	fmt.Println(x.N)

	// a nil pointer element still reports Go's panic, not a trap
	si := []*S{nil}
	func() {
		defer func() { fmt.Println("fieldnil", recover() != nil) }()
		si[0].N++
	}()
}
