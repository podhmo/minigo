package main

import (
	"fmt"
	"strings"
)

// $GOROOT/test/fixedbugs/issue26094.go — two func-local `type T`s in the
// same package are distinct types with identical spellings. Go's failed-
// assertion panic disambiguates them with "(types from different
// scopes)"; the comma-ok form and the static-name slot must also behave.
var X interface{}

type T struct{}

func check(label string) {
	p, ok := recover().(error)
	if ok && strings.Contains(p.Error(), "different scopes") {
		fmt.Println(label, "scopes")
		return
	}
	fmt.Println(label, "unmatched:", p)
}

func F1() {
	type T struct{}
	X = T{}
}

func F2() {
	type T struct{}
	defer check("F2")
	_ = X.(T)
}

func commaOK() {
	type T struct{}
	v, ok := X.(T)
	fmt.Println("comma-ok", ok, v == T{})
}

func main() {
	F1()      // X holds F1's T
	F2()      // asserting F2's T panics with the scopes suffix
	commaOK() // the ok form returns false without panicking
	X = T{}   // now X holds the package-level T
	_ = X.(T)
	fmt.Println("done")
}
