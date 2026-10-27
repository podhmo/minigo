package main

import "fmt"

// An alias IS its target type: a typed const declared with an alias
// (`const a A = 250` with `type A = uint8`) and a conversion `A(3)` are
// uint8 values — %T spells the target, and an interface holding one
// equals uint8(250).

type NM int8
type A = uint8
type C = NM
type I = int

const a A = 250
const c C = 3
const k I = 5

func main() {
	fmt.Printf("%T %T %T\n", a, c, k) // uint8 main.NM int
	var x any = a
	fmt.Println(x == uint8(250), k/2.0)          // true 2
	fmt.Printf("%T %T %v\n", A(3), C(4), A(3)+a) // uint8 main.NM 253
	var y A = 7
	fmt.Printf("%T\n", y) // uint8
}
