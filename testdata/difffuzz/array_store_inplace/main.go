package main

import "fmt"

func main() {
	freq := make([]uint16, 4)
	a := [4]uint16{1, 2, 3, 4}
	p := (*[4]uint16)(freq)
	*p = a
	fmt.Println(freq, *p)
	var arr [2]int
	q := &arr
	s := arr[:]
	*q = [2]int{5, 6}
	fmt.Println(s, arr)
	arr = [2]int{7, 8}
	fmt.Println(s)
	type box struct{ a [2]int }
	var b box
	bs := b.a[:]
	b.a = [2]int{9, 9}
	fmt.Println(bs)
	c := b
	b.a = [2]int{1, 1}
	fmt.Println(c.a, b.a, bs)
	old := arr
	arr = [2]int{0, 0}
	fmt.Println(old, s)
	pa := &b.a
	*pa = [2]int{4, 4}
	fmt.Println(bs, c.a)
	fn := func(x [2]int) [2]int { x[0] = -1; return x }
	fmt.Println(fn(arr), arr)
}
