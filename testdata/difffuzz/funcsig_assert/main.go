package main

import (
	"context"
	"fmt"
	"strings"
)

type Point struct{ X, Y int }

func (p Point) Move(dx, dy int) Point { return Point{p.X + dx, p.Y + dy} }
func (p *Point) SetX(x int)           { p.X = x }

func up(p Point) Point { return Point{p.X, p.Y + 1} }

type F func(int) int
type Alias = func(int) int

func try(f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic:", r)
		}
	}()
	f()
}

func main() {
	var f func(Point) Point = up

	// mismatched signature: comma-ok false, bare assert panics.
	_, ok := any(f).(func(int) int)
	fmt.Println("f.(func(int) int) ok:", ok)
	try(func() { _ = any(f).(func(int) int) })

	// matching signature asserts and calls through.
	if g, ok := any(f).(func(Point) Point); ok {
		fmt.Println("same sig:", g(Point{1, 2}))
	}

	// a closure keeps its own signature.
	c := func(x int) int { return x + 1 }
	_, ok = any(c).(func(int) int)
	fmt.Println("closure ok:", ok)
	_, ok = any(c).(func(string) int)
	fmt.Println("closure wrong:", ok)

	// bound methods sign without the receiver.
	p := Point{3, 4}
	_, ok = any(p.Move).(func(int, int) Point)
	fmt.Println("bound ok:", ok)
	_, ok = any(p.Move).(func(Point) Point)
	fmt.Println("bound wrong:", ok)

	// method expressions sign with the receiver first.
	_, ok = any(Point.Move).(func(Point, int, int) Point)
	fmt.Println("mexpr ok:", ok)
	_, ok = any(Point.Move).(func(int, int) Point)
	fmt.Println("mexpr wrong:", ok)
	_, ok = any((*Point).SetX).(func(*Point, int))
	fmt.Println("pmexpr ok:", ok)

	// variadic is part of the signature — func(...int) is not func([]int).
	vf := func(ns ...int) int { return len(ns) }
	_, ok = any(vf).(func(...int) int)
	fmt.Println("variadic ok:", ok)
	_, ok = any(vf).(func([]int) int)
	fmt.Println("variadic->slice:", ok)
	sf := func(ns []int) int { return len(ns) }
	_, ok = any(sf).(func(...int) int)
	fmt.Println("slice->variadic:", ok)

	// named func types are distinct from identical anonymous signatures.
	var nf F = func(x int) int { return x }
	_, ok = any(nf).(F)
	fmt.Println("nf.(F):", ok)
	_, ok = any(nf).(func(int) int)
	fmt.Println("nf.(func(int) int):", ok)
	_, ok = any(c).(F)
	fmt.Println("c.(F):", ok)

	// an alias is the anonymous signature itself.
	_, ok = any(c).(Alias)
	fmt.Println("c.(Alias):", ok)

	// host-bound funcs compare signatures too — `any` expands to
	// interface{} inside signatures, like reflect spells it.
	_, ok = any(strings.ToUpper).(func(string) string)
	fmt.Println("ToUpper ok:", ok)
	_, ok = any(strings.ToUpper).(func(int) int)
	fmt.Println("ToUpper wrong:", ok)
	_, ok = any(fmt.Sprintf).(func(string, ...any) string)
	fmt.Println("Sprintf ok:", ok)
	_, ok = any(fmt.Sprintf).(func(string) string)
	fmt.Println("Sprintf wrong:", ok)

	// a host func value of a named type asserts to the typedef, not to
	// the identical anonymous signature.
	_, cancel := context.WithCancel(context.Background())
	_, ok = any(cancel).(context.CancelFunc)
	fmt.Println("cancel ok:", ok)
	_, ok = any(cancel).(func())
	fmt.Println("cancel->func():", ok)

	// a nil func still carries its declared signature.
	var nul func(int) int
	_, ok = any(nul).(func(int) int)
	fmt.Println("nilfunc ok:", ok)
	_, ok = any(nul).(func(string) string)
	fmt.Println("nilfunc wrong:", ok)

	// type switch arms use the same comparison.
	switch v := any(f).(type) {
	case func(Point) Point:
		fmt.Println("tswitch hit:", v(Point{9, 9}))
	case func(int) int:
		fmt.Println("tswitch wrong arm")
	default:
		fmt.Println("tswitch default")
	}

	_ = nf
}
