package main

import (
	"fmt"
	"reflect"
)

type MyErr struct{ msg string }

func (e MyErr) Error() string { return e.msg }

type PtrErr struct{}

func (*PtrErr) Error() string { return "p" }

type WrongSig struct{}

func (WrongSig) Error() int { return 1 }

func try(i int, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	f()
}

func main() {
	et := reflect.TypeOf((*error)(nil)).Elem()
	try(0, func() { fmt.Println("0:", reflect.TypeOf(42).Implements(et)) })
	try(1, func() { fmt.Println("1:", reflect.TypeOf(MyErr{}).Implements(et)) })
	try(2, func() { fmt.Println("2:", reflect.TypeOf(PtrErr{}).Implements(et)) })
	try(3, func() { fmt.Println("3:", reflect.TypeOf(&PtrErr{}).Implements(et)) })
	try(4, func() { fmt.Println("4:", reflect.TypeOf(WrongSig{}).Implements(et)) })
	try(5, func() { fmt.Println("5:", reflect.TypeOf(MyErr{}).AssignableTo(et)) })
	try(6, func() {
		_, ok := reflect.TypeAssert[error](reflect.ValueOf(42))
		fmt.Println("6:", ok)
	})
	try(7, func() {
		v, ok := reflect.TypeAssert[error](reflect.ValueOf(&PtrErr{}))
		fmt.Println("7:", v.Error(), ok)
	})
	try(8, func() {
		m := et.Method(0)
		fmt.Println("8:", m.Name, m.Type, m.Index)
	})
	var e error = MyErr{"x"}
	try(9, func() {
		var e2 error
		reflect.ValueOf(&e2).Elem().Set(reflect.ValueOf(MyErr{"y"}))
		fmt.Println("9:", e2.Error(), e.Error())
	})
}
