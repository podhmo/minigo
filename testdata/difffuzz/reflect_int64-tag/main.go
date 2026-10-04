package main

import (
	"fmt"
	"reflect"
	"time"
)

var r_i0 = 42
var r_u0 = uint(7)

func main() {
	v := reflect.ValueOf(r_i0)
	fmt.Printf("%T %v\n", v.Int(), v.Int())
	u := reflect.ValueOf(r_u0)
	fmt.Printf("%T %v\n", u.Uint(), u.Uint())
	fmt.Printf("%T\n", reflect.ValueOf(int64(9)).Int())
	fmt.Printf("%T\n", reflect.ValueOf(uint64(9)).Uint())
	fmt.Printf("%T\n", v.Int()+1)
	fmt.Printf("%T\n", int64(5))
	fmt.Printf("%T\n", time.Second.Nanoseconds())
	fmt.Printf("%v %v\n", v.Int() == 42, v.Int()+v.Int())
}
