package main

import (
	"errors"
	"fmt"
	"runtime"
)

type ExecError struct {
	Name string
	Err  error
}

func (e ExecError) Error() string { return e.Err.Error() }

func classify(e any) string {
	switch e.(type) {
	case runtime.Error:
		return "runtime.Error"
	case ExecError:
		return "ExecError"
	}
	return "other"
}

func try(f func()) (s string) {
	defer func() { s = classify(recover()) }()
	f()
	return "none"
}

func main() {
	fmt.Println(classify(ExecError{"x", errors.New("boom")}), classify(errors.New("plain")))
	var s []int
	var m map[string]int
	var p *ExecError
	var i any = "str"
	zero := 0
	fmt.Println(try(func() { _ = s[3] }))
	fmt.Println(try(func() { m["a"] = 1 }))
	fmt.Println(try(func() { _ = p.Name }))
	fmt.Println(try(func() { _ = i.(int) }))
	fmt.Println(try(func() { _ = 1 / zero }))
	fmt.Println(try(func() { panic(ExecError{"y", errors.New("e")}) }))
	fmt.Println(try(func() { panic("str") }))
}
