package main

import (
	"fmt"
	"os"
)

type F float64
type S string

func assert(cond bool, msg string) {
	if !cond {
		print("assertion fail: ", msg, "\n")
		panic(1)
	}
}

func main() {
	// interface tag: case operands compare as (type, value) pairs —
	// `case 1:` is int, never the float64(1.0) the tag carries.
	switch i := interface{}(float64(1.0)); i {
	case nil:
		assert(false, "nil")
	case (*int)(nil):
		assert(false, "typed nil")
	case 1:
		assert(false, "int 1")
	case F(1.0):
		assert(false, "F(1.0)")
	case 1.0:
		assert(true, "float64")
	case S("x"):
		assert(false, "S")
	default:
		assert(false, "default")
	}

	// a named type inside any matches only the same named type.
	switch i := interface{}(F(3)); i {
	case 3.0:
		assert(false, "float64")
	case F(3):
		assert(true, "F")
	default:
		assert(false, "default")
	}

	// `var x any` is interface-typed too.
	var j any = "hi"
	switch j {
	case 0:
		assert(false, "int")
	case "hi":
		assert(true, "string")
	}

	// default runs at its source position and falls through onward.
	count := 0
	switch {
	default:
		count++
		fallthrough
	case false:
		count++
	}
	assert(count == 2, "fallthrough from default")

	// os.Exit skips defers entirely and ends the run quietly.
	exited := false
	func() {
		defer func() { exited = true }()
	}()
	fmt.Println("ok", count, exited)
	os.Exit(0)
}
