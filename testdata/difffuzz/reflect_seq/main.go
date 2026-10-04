package main

import (
	"fmt"
	"reflect"
	"sort"
)

type N int

var (
	sl  = []string{"a", "b"}
	arr = [3]int{7, 8, 9}
	m   = map[string]int{"x": 1, "y": 2}
	m1  = map[string]int{"x": 1}
	s   = "héllo" // multibyte é
)

func try(i int, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	f()
}

func main() {
	fmt.Println(reflect.TypeOf(sl).CanSeq(), reflect.TypeOf(sl).CanSeq2())
	fmt.Println(reflect.TypeOf(m).CanSeq(), reflect.TypeOf(m).CanSeq2())
	fmt.Println(reflect.TypeOf(s).CanSeq(), reflect.TypeOf(s).CanSeq2())
	fmt.Println(reflect.TypeOf(0).CanSeq(), reflect.TypeOf(0).CanSeq2())
	fmt.Println(reflect.TypeOf(true).CanSeq(), reflect.TypeOf(arr).CanSeq2())
	fmt.Println(reflect.TypeOf(&arr).CanSeq(), reflect.TypeOf(&arr).CanSeq2())
	fmt.Println(reflect.TypeOf(make(chan int)).CanSeq(), reflect.TypeOf(make(chan int)).CanSeq2())

	// slice: Seq yields indices, Seq2 (i, elem)
	for x := range reflect.ValueOf(sl).Seq() {
		fmt.Println("s1", x.Interface(), x.Kind())
	}
	for i, x := range reflect.ValueOf(sl).Seq2() {
		fmt.Println("s2", i.Interface(), x.Interface())
	}
	// map: single-entry for deterministic order
	for k := range reflect.ValueOf(m1).Seq() {
		fmt.Println("m1", k.Interface())
	}
	for k, v := range reflect.ValueOf(m1).Seq2() {
		fmt.Println("m2", k.Interface(), v.Interface())
	}
	// multi-entry map: order-agnostic aggregation
	var ks []string
	sum := 0
	for k, v := range reflect.ValueOf(m).Seq2() {
		ks = append(ks, k.String())
		sum += int(v.Int())
	}
	sort.Strings(ks)
	fmt.Println("msum", ks, sum)
	// string: byte offsets + runes
	for i := range reflect.ValueOf(s).Seq() {
		fmt.Println("i1", i.Interface())
	}
	for i, r := range reflect.ValueOf(s).Seq2() {
		fmt.Println("i2", i.Interface(), r.Interface())
	}
	// int + named int
	for x := range reflect.ValueOf(3).Seq() {
		fmt.Println("n", x.Interface())
	}
	for x := range reflect.ValueOf(N(2)).Seq() {
		fmt.Printf("N %v %T\n", x.Interface(), x.Interface())
	}
	// ptr-to-array
	for i := range reflect.ValueOf(&arr).Seq() {
		fmt.Println("pa", i.Interface())
	}
	for i, x := range reflect.ValueOf(&arr).Seq2() {
		fmt.Println("pa2", i.Interface(), x.Interface())
	}
	// early stop via direct call — must not iterate the rest
	n := 0
	reflect.ValueOf(m).Seq()(func(k reflect.Value) bool {
		n++
		return false
	})
	fmt.Println("stopped", n)
	// func producer
	f := func(yield func(int) bool) {
		for i := 0; i < 3; i++ {
			if !yield(i * 10) {
				return
			}
		}
	}
	for x := range reflect.ValueOf(f).Seq() {
		fmt.Println("f", x.Interface())
	}
	f2 := func(yield func(string, int) bool) {
		yield("a", 1)
		yield("b", 2)
	}
	for k, x := range reflect.ValueOf(f2).Seq2() {
		fmt.Println("f2", k.Interface(), x.Interface())
	}
	fmt.Println(reflect.TypeOf(f).CanSeq(), reflect.TypeOf(f).CanSeq2())
	fmt.Println(reflect.TypeOf(f2).CanSeq(), reflect.TypeOf(f2).CanSeq2())
	fmt.Println(reflect.TypeOf(func(func(int) bool) {}).CanSeq())
	fmt.Println(reflect.TypeOf(func(func(int, int) bool) {}).CanSeq2())
	fmt.Println(reflect.TypeOf(func(func(int) int) {}).CanSeq()) // yield not bool
	fmt.Println(reflect.TypeOf(func(int) {}).CanSeq())           // param not func
	fmt.Println(reflect.TypeOf(func(func(int) bool) int { return 0 }).CanSeq())

	// bad kinds panic with the type's name
	try(0, func() { reflect.ValueOf(true).Seq() })
	try(1, func() { reflect.ValueOf(1.5).Seq() })
	try(2, func() { reflect.ValueOf(0).Seq2() })
	try(3, func() { reflect.ValueOf(make(chan int)).Seq2() })
	try(4, func() { reflect.Value{}.Seq() })
	try(5, func() { reflect.ValueOf(&sl).Seq() })
}
