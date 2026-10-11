package main

import (
	"fmt"
	"reflect"
	"time"
)

// TypeOf spells a liveness proxy box as its public type (*time.Timer);
// a Method Func invoked through reflect must unwrap to the real timer
// rather than die on assignability.
func main() {
	t := time.NewTimer(50 * time.Millisecond)
	defer t.Stop()
	rt := reflect.TypeOf(t)
	fmt.Println(rt, rt.NumMethod())
	mv := rt.Method(0).Func
	fmt.Println(mv.Call([]reflect.Value{reflect.ValueOf(t), reflect.ValueOf(10 * time.Millisecond)}))
}
