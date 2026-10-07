package main

import "fmt"

// $GOROOT/test/fixedbugs/issue23545.go — a named array type compares
// element-wise against its anonymous underlying spelling, like scalars
// already do (any(MyInt(5)) == any(5) reads true here too).
const Size = 32

type OutputID [Size]interface{}

func dummyID(x int) [Size]interface{} {
	var out [Size]interface{}
	out[0] = x
	return out
}

//go:noinline
func Get() OutputID {
	return dummyID(1234)
}

func main() {
	a := Get()
	fmt.Println(a == dummyID(1234))
	fmt.Println(a != dummyID(9999))
	// uncomparable elements still panic through the interface pair
	type bad [2][]int
	var x, y bad
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("trapped")
		}
	}()
	fmt.Println(any(x) == any(y))
}
