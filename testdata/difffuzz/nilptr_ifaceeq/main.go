package main

import "fmt"

// A typed nil pointer boxed in an interface is not equal to a nil
// interface value. $GOROOT/test/fixedbugs/issue19911.go.

type ET struct{}

func (*ET) Error() string { return "err" }

func main() {
	fmt.Println((*ET)(nil) == error(nil))
	fmt.Println((*ET)(nil) != error(nil))
}
