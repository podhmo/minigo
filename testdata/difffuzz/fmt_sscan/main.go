package main

import (
	"bytes"
	"fmt"
)

type MyInt int

func main() {
	var c complex128
	var i int
	var s string
	var f float64
	n, err := fmt.Sscan("3+4i 7 hello 2.5", &c, &i, &s, &f)
	fmt.Println(n, err, c, i, s, f)
	n, err = fmt.Sscanf("x=2.5", "x=%g", &f)
	fmt.Println(n, err, f)
	n, err = fmt.Sscanln("42", &i)
	fmt.Println(n, err, i)
	n, err = fmt.Sscan("zz", &i)
	fmt.Println(n, err != nil, i)
	var b bytes.Buffer
	n, err = fmt.Fscan(&b, &i)
	fmt.Println(n, err != nil, i)
	var m MyInt
	n, err = fmt.Sscan("5", &m)
	fmt.Println(n, err, m)
	var m2 MyInt = m
	fmt.Println(m2)
	n, err = fmt.Sscan("x")
	fmt.Println(n, err)
}
