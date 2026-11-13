package main

import (
	"fmt"
	"reflect"
	"strings"
)

// reflect.MakeFunc on a variadic signature hands the callback its
// variadic tail as ONE []T Value, never as flat arguments: nil when a
// direct call passes no extras, the caller's own slice for `f(xs...)` and
// Value.CallSlice (so the callback aliases it), and a fresh non-nil slice
// for Value.Call.

func show(tag string, args []reflect.Value) {
	s := args[1]
	fmt.Println(tag, len(args), s.Kind(), s.Type(), s.Len(), s.IsNil(), s.Interface())
}

func main() {
	var fn func(string, ...int) string
	mf := reflect.MakeFunc(reflect.TypeOf(fn), func(args []reflect.Value) []reflect.Value {
		show("fn", args)
		if s := args[1]; s.Len() > 0 {
			s.Index(0).Set(reflect.ValueOf(100))
		}
		return []reflect.Value{reflect.ValueOf(strings.ToUpper(args[0].String()))}
	})
	reflect.ValueOf(&fn).Elem().Set(mf)

	// direct calls: no extras is a nil slice, extras are packed, a
	// spread passes the caller's slice itself
	fmt.Println(fn("a"))
	fmt.Println(fn("b", 1, 2))
	xs := []int{7, 8, 9}
	fmt.Println(fn("c", xs...))
	fmt.Println(xs)
	var nilxs []int
	fmt.Println(fn("d", nilxs...))
	empty := []int{}
	fmt.Println(fn("e", empty...))

	// Value.Call folds the extras into a fresh non-nil slice
	fmt.Println(mf.Call([]reflect.Value{reflect.ValueOf("f")})[0])
	fmt.Println(mf.Call([]reflect.Value{reflect.ValueOf("g"), reflect.ValueOf(5), reflect.ValueOf(6)})[0])

	// Value.CallSlice passes the slice as is
	ys := []int{1, 2, 3}
	fmt.Println(mf.CallSlice([]reflect.Value{reflect.ValueOf("h"), reflect.ValueOf(ys)})[0])
	fmt.Println(ys)
	fmt.Println(mf.CallSlice([]reflect.Value{reflect.ValueOf("i"), reflect.ValueOf(nilxs)})[0])
	fmt.Println(mf.CallSlice([]reflect.Value{reflect.ValueOf("j"), reflect.ValueOf(empty)})[0])

	// CallSlice with a nil slice on an ordinary variadic func spreads
	// nothing
	count := func(xs ...int) int { return len(xs) }
	fmt.Println(reflect.ValueOf(count).CallSlice([]reflect.Value{reflect.ValueOf(nilxs)})[0], reflect.ValueOf(count).CallSlice([]reflect.Value{reflect.ValueOf(ys)})[0])

	// defer and go carry the spread the same way
	func() {
		zs := []int{4, 5}
		defer fn("k", zs...)
		defer fn("l")
	}()
	done := make(chan bool)
	go func() {
		fn("m", 3)
		done <- true
	}()
	<-done

	// a variadic-only signature, and elements that are interfaces
	var g func(...string) int
	gv := reflect.MakeFunc(reflect.TypeOf(g), func(args []reflect.Value) []reflect.Value {
		fmt.Println("g", len(args), args[0].Len(), args[0].Interface())
		return []reflect.Value{reflect.ValueOf(args[0].Len())}
	})
	reflect.ValueOf(&g).Elem().Set(gv)
	fmt.Println(g(), g("x", "y", "z"), g([]string{"p", "q"}...))

	var h func(string, ...any) int
	hv := reflect.MakeFunc(reflect.TypeOf(h), func(args []reflect.Value) []reflect.Value {
		s := args[1]
		for i := 0; i < s.Len(); i++ {
			fmt.Println("h", i, s.Index(i).Kind(), s.Index(i).Elem().Kind())
		}
		return []reflect.Value{reflect.ValueOf(s.Len())}
	})
	reflect.ValueOf(&h).Elem().Set(hv)
	fmt.Println(h("n", 1, "two", nil))

	// a host-typed variadic signature
	hostT := reflect.TypeOf(fmt.Sprintf)
	hf := reflect.MakeFunc(hostT, func(args []reflect.Value) []reflect.Value {
		fmt.Println("sprintf", len(args), args[1].Type(), args[1].Len())
		return []reflect.Value{reflect.ValueOf("res")}
	}).Interface().(func(string, ...any) string)
	fmt.Println(hf("x"), hf("x", 1, "a"), hf("x", []any{2, 3}...))

	// the declared signature still gates Call
	try := func(f func()) {
		defer func() { fmt.Println("panic:", recover()) }()
		f()
	}
	try(func() { mf.Call(nil) })
	try(func() { mf.Call([]reflect.Value{reflect.ValueOf("a"), reflect.ValueOf("not int")}) })
	try(func() {
		mf.CallSlice([]reflect.Value{reflect.ValueOf("a"), reflect.ValueOf(1), reflect.ValueOf(2)})
	})
	try(func() { mf.CallSlice([]reflect.Value{reflect.ValueOf("a"), reflect.ValueOf([]string{"x"})}) })

	// the callback can read the tail through its Go type
	var sum func(...int) int
	sv := reflect.MakeFunc(reflect.TypeOf(sum), func(args []reflect.Value) []reflect.Value {
		t := 0
		for _, v := range args[0].Interface().([]int) {
			t += v
		}
		return []reflect.Value{reflect.ValueOf(t)}
	})
	reflect.ValueOf(&sum).Elem().Set(sv)
	fmt.Println(sum(), sum(1, 2, 3), sum([]int{10, 20}...))

	// untyped constants reach the callback with the declared types, the
	// variadic elements included
	var fl func(string, ...float64) float64
	flv := reflect.MakeFunc(reflect.TypeOf(fl), func(args []reflect.Value) []reflect.Value {
		t := 0.0
		for i := 0; i < args[1].Len(); i++ {
			t += args[1].Index(i).Float()
		}
		fmt.Println("fl", args[0].String(), args[0].Len(), args[1].Type().Elem())
		return []reflect.Value{reflect.ValueOf(t)}
	})
	reflect.ValueOf(&fl).Elem().Set(flv)
	fmt.Println(fl("ab"), fl("cd", 1, 2.5))
}
