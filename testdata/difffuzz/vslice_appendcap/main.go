package main

import "fmt"

// append must keep a virtual slice's logical capacity when the result
// still fits — like Go's growslice, which does not shrink the cap.

func main() {
	s := make([]struct{}, 1, 1<<33)
	fmt.Println(cap(append(s, struct{}{})))
}
