package main

import (
	"fmt"
	"reflect"
)

// FieldByName reports a promoted field's LOCAL offset inside the
// struct that declares it — the same number FieldByIndex gives — not
// the top-level position the embedding chain implies.

type Inner struct {
	A int64
	Y int32
}

type Outer struct {
	X int
	Inner
}

type PtrOuter struct {
	X int
	*Inner
}

type Deep struct {
	D int
	Outer
}

func main() {
	ot := reflect.TypeOf(Outer{})
	f, ok := ot.FieldByName("Y")
	fmt.Println("FieldByName(Y):", f.Offset, f.Index, ok)
	fmt.Println("FieldByIndex([1,1]):", ot.FieldByIndex([]int{1, 1}).Offset)
	fe := ot.Field(1)
	fmt.Println("Field(1) Inner:", fe.Offset, fe.Anonymous)

	pt := reflect.TypeOf(PtrOuter{})
	fp, ok := pt.FieldByName("Y")
	fmt.Println("ptr FieldByName(Y):", fp.Offset, fp.Index, ok)

	dt := reflect.TypeOf(Deep{})
	fd, ok := dt.FieldByName("Y")
	fmt.Println("deep FieldByName(Y):", fd.Offset, fd.Index, ok)
	fa, ok := dt.FieldByName("A")
	fmt.Println("deep FieldByName(A):", fa.Offset, fa.Index, ok)
	_, ok = dt.FieldByName("Nope")
	fmt.Println("missing:", ok)
}
