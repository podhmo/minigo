package main

import "fmt"

type Bad struct{}

func (b Bad) Error() string  { panic("boom") }
func (b Bad) String() string { panic("s-boom") }

type BadPtr struct{ N int }

func (b *BadPtr) Error() string { panic("ptr-boom") }

type GS struct{}

func (g GS) GoString() string { panic("gs-boom") }

type F struct{}

func (f F) Format(s fmt.State, verb rune) { panic("fmt-boom") }

type P2 struct{}

func (p P2) Error() string { panic(42) }

func main() {
	fmt.Println(Bad{})
	fmt.Println(BadPtr{})
	fmt.Println(&BadPtr{})
	var e error = Bad{}
	fmt.Println(e)
	fmt.Printf("%v %s %q %x\n", Bad{}, Bad{}, Bad{}, Bad{})
	fmt.Printf("%#v\n", GS{})
	fmt.Println(F{})
	fmt.Println(P2{})
	fmt.Printf("[%30v]\n", Bad{})
	nilCases()
}

type PtrDeref struct{ Msg string }

func (p *PtrDeref) Error() string { return p.Msg } // derefs nil -> panic

type PtrSafe struct{}

func (p *PtrSafe) Error() string { return "ptr-safe" } // survives nil receiver

func nilCases() {
	var pd *PtrDeref
	fmt.Println(pd)
	var ps *PtrSafe
	fmt.Println(ps)
	var e error = pd
	fmt.Println(e)
	var vp *P2
	fmt.Println(vp)
}
