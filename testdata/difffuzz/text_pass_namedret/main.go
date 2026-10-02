package main

import "fmt"

func f1(x, y, z int) (xx, yy, zz int) {
	return x, y, z
}

func f2() (x, y, z int) {
	return f1(2, 1, 3)
}

func f3() (int, int, int) {
	return f1(4, 5, 6)
}

func main() {
	a, b, c := f2()
	fmt.Println(a, b, c)
	d, e, g := f3()
	fmt.Println(d, e, g)
}
