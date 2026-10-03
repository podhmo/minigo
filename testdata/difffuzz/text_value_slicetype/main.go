package main

import (
	"bytes"
	"fmt"
	"reflect"
)

func main() {
	// append string-spread vs []byte() conversion — same elements,
	// same dynamic type
	a := append([]byte{}, "0"...)
	b := []byte("0")
	fmt.Println("app==conv:", reflect.DeepEqual(a, b))
	// literal vs conversion
	fmt.Println("lit==conv:", reflect.DeepEqual([]byte{48}, b))
	// byte folds to uint8
	fmt.Println("u8==byte:", reflect.DeepEqual([]uint8("0"), b))
	// rune conversion vs literal
	fmt.Println("rune:", reflect.DeepEqual([]rune("x"), []rune{'x'}))
	// host-produced []byte vs literal
	f := bytes.Fields([]byte("a b"))[0]
	fmt.Println("fields:", reflect.DeepEqual(f, []byte{97}))
}
