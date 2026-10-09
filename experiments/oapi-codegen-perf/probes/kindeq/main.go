package main

import (
	"fmt"
	"reflect"
)

const c = reflect.Int

func main() {
	k := reflect.Int
	fmt.Println(k == 2, c == 2)
	var x reflect.Kind = 2
	fmt.Println(k == x)
}
