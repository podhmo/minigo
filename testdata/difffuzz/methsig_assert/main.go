package main

import "fmt"

type S struct{ a int }

func (s *S) Name(x int) int8 { return 1 }

type I1 interface{ Name(x int) int8 }
type I2 interface{ Name(x int64) int8 }
type I3 interface{ Name(x int) int64 }
type I4 interface{ Missing() int }

func try(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println(name, "panicked")
			return
		}
		fmt.Println(name, "no panic")
	}()
	f()
}

func main() {
	var s *S
	var i1 I1 = s
	try("param-mismatch", func() { _ = i1.(I2) })
	try("return-mismatch", func() { _ = i1.(I3) })
	try("missing-method", func() { _ = i1.(I4) })
}
