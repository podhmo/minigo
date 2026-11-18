package main

// Conversion differs from assignment: struct tags are ignored,
// numerics interconvert, int<->string and []byte/[]rune<->string
// round-trip, and slices convert to arrays or array pointers.

import (
	"fmt"
	"reflect"
)

type S1 struct {
	X int `a:"1"`
}
type S2 struct {
	X int `b:"2"`
}

func main() {
	var s2 = S2(S1{X: 1}) // tag mismatch converts, would not assign
	fmt.Println(s2.X)

	fmt.Println(string(65))
	fmt.Println(string([]byte("ab")))
	fmt.Println(len([]byte("abc")), len([]rune("abc")))
	var f float64 = 3.9
	var n int = 7
	fmt.Println(int(f), float64(n))

	var sl = []int{1, 2}
	arr := [2]int(sl)
	fmt.Println(arr)
	parr := (*[2]int)(sl)
	fmt.Println(*parr)

	reflectChecks()
}

type S3 struct {
	X string
}
type B byte

func reflectChecks() {
	it := reflect.TypeOf(int(0))
	st := reflect.TypeOf("")
	fmt.Println("int->string:", it.ConvertibleTo(st)) // true
	fmt.Println("string->[]byte:", st.ConvertibleTo(reflect.TypeOf([]byte{})))
	fmt.Println("string->[]int:", st.ConvertibleTo(reflect.TypeOf([]int{})))
	fmt.Println("int->float:", it.ConvertibleTo(reflect.TypeOf(float64(0))))
	s1t, s2t := reflect.TypeOf(S1{}), reflect.TypeOf(S2{})
	fmt.Println("S1->S2:", s1t.ConvertibleTo(s2t))          // true, tags ignored
	fmt.Println("S1 assignable S2:", s1t.AssignableTo(s2t)) // false
	s3t := reflect.TypeOf(S3{})
	fmt.Println("S1->S3:", s1t.ConvertibleTo(s3t)) // false, field types differ
	fmt.Println("[]byte->string:", reflect.TypeOf([]byte{}).ConvertibleTo(st))
	fmt.Println("[]B->string:", reflect.TypeOf([]B{}).ConvertibleTo(st)) // elem underlying byte
	fmt.Println("[]int->string:", reflect.TypeOf([]int{}).ConvertibleTo(st))
	fmt.Println("nested tag:", reflect.TypeOf(struct{ Y S1 }{}).ConvertibleTo(reflect.TypeOf(struct{ Y S2 }{})))
	fmt.Println("anon nested tag:", reflect.TypeOf(struct {
		Y struct {
			X int `a:"1"`
		}
	}{}).ConvertibleTo(reflect.TypeOf(struct {
		Y struct {
			X int `b:"2"`
		}
	}{}))) // true: tags ignored inside anonymous structs
	fmt.Println("renamed field:", reflect.TypeOf(struct{ A int }{}).ConvertibleTo(reflect.TypeOf(struct{ B int }{})))
}
