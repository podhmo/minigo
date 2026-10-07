package main

import (
	"fmt"
	"math/big"
)

// $GOROOT/test/fixedbugs/issue9604b.go — a script-defined
// fmt.Formatter's Format(fmt.State, rune) method handles every verb
// but %T/%p, writing to the live printer. minigo only consulted the
// Stringer family, so %d on a script *big.Int rendered the struct's
// fields (&{%!d(bool=...) [...]}).
func main() {
	fmt.Printf("%d %v %s %x\n", big.NewInt(-42), big.NewInt(-42), big.NewInt(-42), big.NewInt(-42))
	fmt.Printf("%x\n", big.NewInt(255))
	fmt.Println(big.NewInt(123))
}
