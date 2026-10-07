package main

import "fmt"

// stack-579 review B6 — a value method promoted into an interface
// holding nil *T splits two panic flavors by binding site: a
// callee-position select on a function-local name dispatches the *T→T
// wrapper directly (nil pointer dereference), while a package var,
// container element, or lazily bound method value reports the checked
// wrapper's "value method ... called using nil" text.
// difffuzz: pkg-level var case is pinned at valuemethod_nilptr.

type T struct{}

func (T) M() {}

type I interface{ M() }

var pt *T
var gi I = pt

func chk(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: %v\n", name, r)
		}
	}()
	f()
}

func main() {
	chk("local conv", func() {
		var i I = (*T)(nil)
		i.M()
	})
	chk("local via ptr", func() {
		var t *T
		var i I = t
		i.M()
	})
	chk("pkg iface", func() { gi.M() })
	chk("pkg ptr", func() { pt.M() })
	chk("method value", func() {
		var i I = pt
		f := i.M
		f()
	})
	chk("defer iface", func() {
		var i I = pt
		defer i.M()
	})
	chk("chained", func() {
		var i I = pt
		var j I = i
		j.M()
	})
	chk("map elem", func() {
		m := map[string]I{"k": pt}
		m["k"].M()
	})
	chk("slice elem", func() {
		s := []I{pt}
		s[0].M()
	})
	chk("field", func() {
		var s struct{ i I }
		s.i = pt
		s.i.M()
	})
	chk("captured", func() {
		var i I = pt
		g := func() { i.M() }
		g()
	})
	chk("paren", func() {
		var i I = pt
		(i).M()
	})
}
