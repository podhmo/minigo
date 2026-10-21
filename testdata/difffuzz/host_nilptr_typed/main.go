package main

// A nil *T returned by a host function (os.Open's file on error) is a
// typed nil: it compares equal to nil, keeps its dynamic type once
// stored in an interface, asserts and type-switches to *os.File, and
// dispatches *File's methods on the nil receiver (ErrInvalid).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
)

type holder struct {
	F *os.File
	A any
}

func isNil(f *os.File) bool { return f == nil }
func isNilAny(x any) bool   { return x == nil }

func main() {
	f, err := os.Open("/nonexistent/minigo-host-nilptr")
	fmt.Println(f == nil, f != nil, err != nil)
	if f == nil {
		fmt.Println("nil file")
	}
	fmt.Println(f)
	fmt.Printf("%v|%T|%#v\n", f, f, f)

	// nil-receiver methods
	fmt.Println(f.Close() == os.ErrInvalid)
	_, err = f.Stat()
	fmt.Println(err)
	n, err := f.Write([]byte("x"))
	fmt.Println(n, err)

	// an interface holding the typed nil is not nil
	var r io.Reader = f
	fmt.Println(r == nil, r != nil)
	p, ok := r.(*os.File)
	fmt.Println(p == nil, ok)
	_, ok = r.(io.Closer)
	fmt.Println(ok)
	switch r.(type) {
	case nil:
		fmt.Println("switch nil")
	case *os.File:
		fmt.Println("switch *os.File")
	}
	fmt.Printf("%v %T\n", r, r)

	// comparisons against script-side nils of the same type
	var g *os.File
	var x any = (*os.File)(nil)
	var y any = g
	var z any = f
	fmt.Println(f == g, z == x, z == y, x == y)
	fmt.Println(isNil(f), isNilAny(f), isNilAny(g))
	g = f
	fmt.Println(g == nil)

	// storage keeps the typed nil
	h := holder{F: f, A: f}
	fmt.Println(h.F == nil, h.A == nil)
	fmt.Printf("%v %+v\n", h, h)
	m := map[string]*os.File{"a": f}
	s := []*os.File{f}
	fmt.Println(m["a"] == nil, s[0] == nil)
	ch := make(chan *os.File, 1)
	ch <- f
	fmt.Println(<-ch == nil)

	// back to the host
	fmt.Println(reflect.TypeOf(f), reflect.ValueOf(f).IsNil())
	b, err := json.Marshal(f)
	fmt.Println(string(b), err)
	n64, err := io.Copy(io.Discard, f)
	fmt.Println(n64, err)
	sc := bufio.NewScanner(f)
	fmt.Println(sc.Scan(), sc.Err())
}
