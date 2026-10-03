package main

import (
	"fmt"
	"math"
)

const (
	two53   = 1.0 * (1 << 53)
	two64   = 1.0 * (1 << 64)
	two128  = two64 * two64
	two1024 = two128 * two128 * two128 * two128 * two128 * two128 * two128 * two128
	ulp64   = two1024 / two53
	max64   = two1024 - ulp64
)

func bits(x interface{}) interface{} {
	switch x := x.(type) {
	case float32:
		return uint64(math.Float32bits(x))
	case float64:
		return math.Float64bits(x)
	}
	return 0
}

func main() {
	// a float64 in an interface must not assert to float32 —
	// float_lit2's bits() helper hit this
	var i interface{} = float64(max64 - ulp64)
	fmt.Printf("%x\n", bits(i))
	var j interface{} = 7
	if _, ok := j.(int8); ok {
		fmt.Println("int asserted to int8")
	}
	if _, ok := j.(int64); ok {
		fmt.Println("int asserted to int64")
	}
	if _, ok := j.(int); ok {
		fmt.Println("int ok")
	}
	var k interface{} = int64(9)
	if _, ok := k.(int); ok {
		fmt.Println("int64 asserted to int")
	}
	if _, ok := k.(int64); ok {
		fmt.Println("int64 ok")
	}
}
