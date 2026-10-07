package main

import "fmt"

func main() {
	freq := make([]uint16, 6)
	a := [4]uint16{1, 2, 3, 4}
	b := [2]uint16{7, 8}
	*(*[4]uint16)(freq[:]) = a
	*(*[2]uint16)(freq[4:]) = b
	fmt.Println(freq)
	p := (*[3]uint16)(freq)
	p[0] = 99
	fmt.Println(freq[0], len(p))
	arr := [3]int{1, 2, 3}
	s := arr[:]
	q := (*[3]int)(s)
	q[2] = 30
	fmt.Println(arr)
	c := [2]int(s[:2])
	c[0] = 100
	fmt.Println(arr, c)
}
