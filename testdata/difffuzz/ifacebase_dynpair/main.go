package main

import "fmt"

type A any
type E error
type C chan int
type N int

type myErr struct{}

func (myErr) Error() string { return "x" }

// a declared-over-interface tag is never the dynamic type: the payload's
// (type, value) pair is what map keys, assertions and method dispatch see.
func main() {
	m := map[any]int{}
	m[C(make(chan int, 1))] = 1
	var a A = C(make(chan int, 1)) // distinct chan value
	fmt.Println(m[a])
	ch := C(make(chan int, 1))
	m2 := map[any]int{ch: 2}
	var a2 A = ch
	fmt.Println(m2[a2])

	var a3 A = N(1)
	if v, ok := a3.(int); ok {
		fmt.Println("int", v)
	} else {
		fmt.Println("no")
	}
	if _, ok := a3.(N); ok {
		fmt.Println("N")
	}
	if _, ok := a3.(A); ok {
		fmt.Println("A")
	}

	var e E = myErr{}
	if _, ok := e.(E); ok {
		fmt.Println("E", e.Error())
	}
	if v, ok := e.(myErr); ok {
		fmt.Println("myErr", v.Error())
	}
	var e2 E
	fmt.Println(e2 == nil)
	reqs()
	ptrs()
	sigs()
	unexported()
	if _, ok := e2.(E); ok {
		fmt.Println("nil asserts")
	}
	embeds()
}

// a struct embedding a defined-over-interface type promotes the
// underlying interface's methods: S satisfies E and selects Error
// through the stored interface value, while two iface embeds at the
// same depth contest the name away.
type W E
type S struct{ E }
type SW struct{ W }
type D struct {
	E
	error
}

func embeds() {
	var s S = S{E: myErr{}}
	if _, ok := any(s).(E); !ok {
		fmt.Println("bad S")
	} else {
		fmt.Println("S is E")
	}
	fmt.Println(s.Error())
	var sw SW = SW{W: myErr{}}
	if _, ok := any(sw).(E); !ok {
		fmt.Println("bad SW")
	} else {
		fmt.Println("SW is E")
	}
	var d D = D{E: myErr{}, error: myErr{}}
	if _, ok := any(d).(E); ok {
		fmt.Println("bad D")
	} else {
		fmt.Println("D not E")
	}
}

func reqs() {
	var a any = 42
	if _, ok := a.(E); ok {
		fmt.Println("bad")
	} else {
		fmt.Println("42 is not E")
	}
	var e2 E
	var a2 A = e2
	fmt.Println(a2 == nil)
	e3 := e2
	fmt.Println(e3 == nil)
}

// a pointer to a declared-over-interface type has an empty method set
// (gc: *E carries no methods).
func ptrs() {
	var pe *E
	if _, ok := any(pe).(error); ok {
		fmt.Println("bad *E")
	} else {
		fmt.Println("*E not error")
	}
	var pw *W
	if _, ok := any(pw).(error); ok {
		fmt.Println("bad *W")
	} else {
		fmt.Println("*W not error")
	}
}

// a promoted requirement keeps its signature — a same-name method with
// the wrong signature does not satisfy.
type BadSig interface{ Error() int }

func sigs() {
	var e E = myErr{}
	if _, ok := any(e).(BadSig); ok {
		fmt.Println("bad e")
	} else {
		fmt.Println("e not BadSig")
	}
	type J interface{ E }
	var j J = myErr{}
	if _, ok := any(j).(BadSig); ok {
		fmt.Println("bad j")
	} else {
		fmt.Println("j not BadSig")
	}
	if _, ok := any(j).(interface{ Error() string }); !ok {
		fmt.Println("bad j2")
	} else {
		fmt.Println("j is Stringer")
	}
}

// an unexported requirement also promotes under its plain name, so the
// method expression and the selector reach the stored value.
type I2 interface{ hidden() string }
type h struct{}

func (h) hidden() string { return "h!" }

type S2 struct{ I2 }

func unexported() {
	s := S2{I2: h{}}
	var f func(S2) string = S2.hidden
	fmt.Println(f(s))
	fmt.Println(s.hidden())
}
