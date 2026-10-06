package main

import "fmt"

func main() {
	r := []struct{}{struct{}{}, struct{}{}}
	z := make([]struct{}, 1<<33)
	out := append(r, z...)
	fmt.Println(len(out), cap(out))
	out2 := append(z, r...)
	fmt.Println(len(out2), cap(out2))
}
