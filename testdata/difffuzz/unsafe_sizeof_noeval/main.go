package main

import (
	"fmt"
	"math"
	"unicode"
	"unsafe"
)

const PJ = 5
const PK = PJ + 1

// a typed const's operators wrap at the declared width, so ^KC is
// 255 — the fold cannot express that in a bare value and leaves it
// to evaluation.
const KC uint8 = 0
const UKC = ^KC >> 7 & 3

// a declared iota shadows the predeclared one for the whole package.
const iota = 10
const IK = iota
const PK2 = 4

var shadowed = [...]byte{PK2: 0, math.MaxInt8: 0}

// a typed-const key on a package var bakes its value (a same-named
// local cannot rebind it), while an operator on it emits and
// evaluates in the decl's package scope at run time.
var ptyped = [...]byte{KC: 0}
var ptypedExpr = [...]byte{^KC >> 7 & 3: 0}

type Pair struct {
	A int64
	B int32
}

type Box int64

var boxed Box

var boxedSlice []Box

func f() int { fmt.Println("f ran"); return 1 }

func main() {
	var p *Pair
	s := unsafe.Sizeof(f())
	fmt.Println("after Sizeof")
	fmt.Printf("%T %v\n", s, s)
	fmt.Printf("%T %v\n", unsafe.Sizeof([3]int64{}), unsafe.Sizeof([3]int64{}))
	fmt.Printf("%v\n", unsafe.Sizeof(p.A)) // field of nil ptr: must not eval
	fmt.Printf("%T %v\n", unsafe.Alignof(p.B), unsafe.Alignof(p.B))
	fmt.Printf("%T %v\n", unsafe.Offsetof(p.B), unsafe.Offsetof(p.B))
	var err error = fmt.Errorf("x")
	fmt.Printf("%v\n", unsafe.Sizeof(err))
	fmt.Printf("%v\n", unsafe.Sizeof(1))
	fmt.Printf("%v\n", unsafe.Sizeof("abc"))
	var arr [4]int32
	fmt.Printf("%v %v\n", unsafe.Sizeof(arr), unsafe.Sizeof(arr[2]))
	// an array operand's subslice is a slice header; a host struct's
	// layout lives in reflect; an ellipsis literal's length is the
	// element count.
	fmt.Printf("%v %v\n", unsafe.Sizeof(arr[:]), unsafe.Alignof(arr[:]))
	var r unicode.Range16
	fmt.Printf("%v %v\n", unsafe.Sizeof(r), unsafe.Offsetof(r.Hi))
	fmt.Printf("%v %v\n", unsafe.Sizeof([...]int{1, 2}), unsafe.Sizeof([...]int{9: 1}))
	// named-const and non-decimal keys fold to the length too, and
	// the elements still never evaluate.
	const K = 5
	const J = K + 2
	const max = 99 // shadows the builtin
	fmt.Printf("%v %v\n", unsafe.Sizeof([...]int{K: f()}), unsafe.Sizeof([...]int{0x10: f(), 1}))
	fmt.Printf("%v %v\n", unsafe.Sizeof([...]int{J: f()}), unsafe.Sizeof([...]int{uint8(255): f()}))
	fmt.Printf("%v %v\n", unsafe.Sizeof([...]int{max: f()}), unsafe.Sizeof([...]int{len("abc"): f()}))
	// keys the compile fold cannot see still keep the elements
	// unevaluated: an imported const, a typed-constant expression, a
	// package const whose initializer names a shadowed name, and
	// unkeyed positions after a keyed one.
	const PJ = 9
	fmt.Printf("%v %v\n", unsafe.Sizeof([...]int{math.MaxInt8: f()}), unsafe.Sizeof([...]int{^uint8(0) >> 7 & 3: f()}))
	fmt.Printf("%v %v\n", unsafe.Sizeof([...]int{PK: f()}), unsafe.Sizeof([...]int{3: f(), f(), 10: f(), f()}))
	// a declared iota shadows the predeclared one; a re-declared const
	// folds its initializer against the outer binding; a narrow typed
	// key does not truncate the position count; and a package var's
	// literal keys resolve in the decl's scope even where a local
	// name shadows them.
	fmt.Printf("%v %v\n", unsafe.Sizeof([...]byte{IK: 0}), unsafe.Sizeof([...]byte{uint8(255): 0, 0}))
	{
		const PJ = PJ + 1
		fmt.Printf("%v\n", unsafe.Sizeof([...]byte{PJ: 0}))
	}
	const PK2 = "shadow"
	fmt.Printf("%v\n", unsafe.Sizeof(shadowed))
	// a typed-const key emits rather than folding, so its width
	// still wraps: ^KC is 255, and a named alias of the
	// expression keys position 1.
	fmt.Printf("%v %v\n", unsafe.Sizeof([...]byte{uint8(0): 0, ^KC >> 7 & 3: 0}), unsafe.Sizeof([...]byte{UKC: 0, 0}))
	{
		const KC = "shadow"
		fmt.Printf("%v %v\n", unsafe.Sizeof(ptyped), unsafe.Sizeof(ptypedExpr))
	}
	// min/max type by the first TYPED argument — untyped constants
	// convert to it — not by argument position.
	fmt.Printf("%v %v %v\n", unsafe.Sizeof(min(1, int8(2))), unsafe.Sizeof(min(int64(1), 2)), unsafe.Sizeof(min(1, 2)))
	// a package var's declared type resolves in the decl's scope —
	// a use-site local type of the same name cannot shadow it.
	{
		type Box int8
		fmt.Printf("%v %v\n", unsafe.Sizeof(boxed), unsafe.Sizeof(boxedSlice))
		// an inferred binding keeps its decl's file: x's type is
		// the package Box the initializer spelled, not the local.
		x := min(1, boxed)
		fmt.Printf("%v\n", unsafe.Sizeof(x))
	}
}
