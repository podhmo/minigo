package main

import "fmt"

func main() {
	z := make([]struct{}, 3)
	fmt.Println(cap(append(z, struct{}{})))
	fmt.Println(cap(append(z, struct{}{}, struct{}{})))
	w := make([]struct{}, 3, 5)
	fmt.Println(cap(append(w, struct{}{})))
	fmt.Println(len(append(w, struct{}{})))
}
