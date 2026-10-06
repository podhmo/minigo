package main

import (
	"fmt"
	"reflect"
)

// An absent struct tag and an empty tag `""` are the SAME type:
// AssignableTo must hold. $GOROOT/test/fixedbugs/issue15439.go.

func main() {
	a := reflect.TypeOf((*struct{ x int })(nil)).Elem()
	b := reflect.TypeOf((*struct {
		x int ""
	})(nil)).Elem()
	fmt.Println(b.AssignableTo(a), a == b)
}
