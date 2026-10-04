package main

import (
	"fmt"
	"reflect"
)

type NI int
type NS []byte

var (
	i    = 42
	f    = 1.5
	s    = "ab"
	b    = []byte("abcd")
	ns   = NS("wxyz")
	ui   = uint(7)
	cx   = complex(1, 2)
	arr4 = [4]byte{}
	parr = &[2]byte{}
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	fmt.Printf("%d: %v\n", i, f())
}

func main() {
	// the Can* kind queries
	vi := reflect.ValueOf(i)
	fmt.Println(vi.CanInt(), vi.CanUint(), vi.CanFloat(), vi.CanComplex())
	vf := reflect.ValueOf(f)
	fmt.Println(vf.CanInt(), vf.CanFloat(), vf.CanComplex())
	vu := reflect.ValueOf(ui)
	fmt.Println(vu.CanInt(), vu.CanUint())
	vc := reflect.ValueOf(cx)
	fmt.Println(vc.CanComplex(), vc.CanFloat())
	fmt.Println(reflect.Value{}.CanInt(), reflect.Value{}.CanFloat())
	// CanConvert: statically-convertible types
	fmt.Println(vi.CanConvert(reflect.TypeOf(f)), vi.CanConvert(reflect.TypeOf("")))
	fmt.Println(vi.CanConvert(reflect.TypeOf(NI(0))))
	fmt.Println(reflect.ValueOf(s).CanConvert(reflect.TypeOf([]byte{})))
	fmt.Println(reflect.ValueOf(s).CanConvert(reflect.TypeOf([]rune{})))
	fmt.Println(reflect.ValueOf(b).CanConvert(reflect.TypeOf("")))
	// runtime-dependent: slice -> array / *array needs the length
	vb := reflect.ValueOf(b)
	fmt.Println(vb.CanConvert(reflect.TypeOf(arr4)))
	fmt.Println(vb.CanConvert(reflect.TypeOf([5]byte{})))
	fmt.Println(vb.CanConvert(reflect.TypeOf(parr)))
	fmt.Println(vb.CanConvert(reflect.TypeOf(&[6]byte{})))
	vn := reflect.ValueOf(ns)
	fmt.Println(vn.CanConvert(reflect.TypeOf(arr4)), vn.CanConvert(reflect.TypeOf("")))
	fmt.Println(reflect.ValueOf(arr4).CanConvert(reflect.TypeOf(arr4)))
	fmt.Println(reflect.ValueOf(arr4).CanConvert(reflect.TypeOf(b)))
	// the conversions CanConvert reports on
	a := vb.Convert(reflect.TypeOf([4]byte{}))
	fmt.Printf("%v %T\n", a.Interface(), a.Interface())
	p := vb.Convert(reflect.TypeOf(&[4]byte{}))
	fmt.Printf("%v %T\n", p.Elem().Interface(), p.Interface())
	try(0, func() any { return vb.Slice(0, 2).Convert(reflect.TypeOf([4]byte{})) })
	try(1, func() any { return vb.Slice(0, 2).Convert(reflect.TypeOf(&[4]byte{})) })
	fmt.Println(vb.Type().ConvertibleTo(reflect.TypeOf([2]byte{})), vb.Type().ConvertibleTo(reflect.TypeOf(&[2]byte{})))
	p.Interface().(*[4]byte)[0] = 'Z'
	fmt.Println(string(b))
	// zero Value CanConvert panics on Type, not CanConvert
	try(2, func() any { return reflect.Value{}.CanConvert(reflect.TypeOf(0)) })
}
