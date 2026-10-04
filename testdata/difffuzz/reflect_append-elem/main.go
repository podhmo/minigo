package main

import (
	"fmt"
	"reflect"
)

type RInner struct{ X int }
type ROuter struct {
	RInner
	B int
}
type RTag struct {
	A int    `json:"a"`
	B string `json:"b,omitempty"`
}
type RNamed int
type RHidden struct {
	x int
	Y int
}

// RStr is the only prelude type carrying a method set — value and
// pointer receivers — so NumMethod/Method(i)/MethodByName/Method.Func
// are exercised on a script type, not just host interfaces.
type RStr string

func (s RStr) String() string { return "S<" + string(s) + ">" }
func (s RStr) Twice() string  { return string(s) + string(s) }
func (s *RStr) Yell() string  { return "[" + string(*s) + "]" }

type RNSlice []int
type RNStr string
type RNMap map[string]int

var (
	r_i0     = 42
	r_s0     = "hi"
	r_f0     = 1.5
	r_b0     = true
	r_slice  = []int{3, 4, 5}
	r_arr    = [3]int{1, 2, 3}
	r_map    = map[string]int{"a": 1, "b": 2}
	r_struct = RTag{A: 7, B: "t"}
	r_outer  = ROuter{RInner: RInner{X: 9}, B: 8}
	r_hidden = RHidden{x: 1, Y: 2}
	r_named  = RNamed(5)
	r_iface  = any("iface")
	r_nilptr = (*int)(nil)
	r_nilmap = map[string]int(nil)
	r_bytes  = []byte("abc")
	r_chan   = make(chan int, 1)
	r_func   = func(x int) int { return x * 2 }
	r_funcv  = func(xs ...int) int {
		s := 0
		for _, x := range xs {
			s += x
		}
		return s
	}
	r_empty    = any(nil)
	r_strslice = []string{"p", "q"}
	r_sptr     = &r_struct
	r_nested   = [][]int{{1, 2}, {3}}
	r_mapslice = map[string][]int{"k": {4, 5}}
	r_rstr     = RStr("rs")
	r_nslice   = RNSlice{6, 7}
	r_nstr     = RNStr("nm")
	r_nmap     = RNMap{"m": 1}
)

// r_mkchan gives each probe its own buffered, prefilled channel — a
// shared channel would carry Sends across probes (cap-1 would block the
// whole program) and a bare Recv could block forever.
func r_mkchan() chan int { c := make(chan int, 32); c <- 7; return c }

// rTryRecvV / rTrySendB probe the non-blocking channel ops as values
// (Recv itself can block, so it is never probed).
func rTryRecvV(v reflect.Value) reflect.Value {
	r, ok := v.TryRecv()
	if !ok {
		return reflect.ValueOf("none")
	}
	return r
}

func rTrySendB(v reflect.Value) string {
	return fmt.Sprintf("%v", v.TrySend(reflect.ValueOf(9)))
}

// rAssertInt / rAssertStr probe reflect.TypeAssert[T] — a two-result
// generic call the chain model cannot express directly.
func rAssertInt(v reflect.Value) string {
	n, ok := reflect.TypeAssert[int](v)
	return fmt.Sprintf("%d %v", n, ok)
}

func rAssertStr(v reflect.Value) string {
	s, ok := reflect.TypeAssert[string](v)
	return fmt.Sprintf("%q %v", s, ok)
}

// rMethodType probes Type.MethodByName — also a two-result call.
func rMethodType(t reflect.Type) reflect.Type {
	m, ok := t.MethodByName("String")
	if !ok {
		return t
	}
	return m.Type
}

// rFieldType probes Type.FieldByName — a two-result call like
// MethodByName; the miss path just returns the receiver.
func rFieldType(t reflect.Type) reflect.Type {
	f, ok := t.FieldByName("A")
	if !ok {
		return t
	}
	return f.Type
}

// rAppendV / rAppendSliceV / rCopyI probe reflect.Append/AppendSlice/Copy
// — package-level slice ops that take the chain value as their first
// arg. Append returns a fresh Value (the seed's header is untouched),
// Copy returns an int (stringified to keep %T out of the output).
func rAppendV(v reflect.Value) reflect.Value {
	return reflect.Append(v, reflect.ValueOf(9))
}

func rAppendSliceV(v reflect.Value) reflect.Value {
	return reflect.AppendSlice(v, reflect.ValueOf([]int{1, 2}))
}

func rCopyI(v reflect.Value) string {
	return fmt.Sprintf("%v", reflect.Copy(v, reflect.ValueOf([]int{1, 2})))
}

var ()

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func id[T any](x T) T { return x }

func main() {
	try(0, func() any {
		v0 := reflect.ValueOf(r_bytes)
		v1 := rAppendSliceV(v0)
		v2 := fmt.Sprintf("%#v", v1.Interface())
		return v2
	})
}
