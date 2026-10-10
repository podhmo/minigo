package main

import (
	"bytes"
	"fmt"
	"reflect"
	"time"
)

// A typedef-backed host type must answer Implements/AssignableTo
// against interface types from its real host method set — methodSet
// only enumerates declared script methods, so *bytes.Buffer used to
// read as implementing nothing.
func main() {
	sr := reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
	st := reflect.TypeOf((*bytes.Buffer)(nil))
	fmt.Println("ptr implements:", st.Implements(sr))
	fmt.Println("value implements:", reflect.TypeOf(bytes.Buffer{}).Implements(sr))
	fmt.Println("assignable:", st.AssignableTo(sr))

	var b *bytes.Buffer
	v := reflect.ValueOf(&b).Elem()
	fmt.Println("vtype assignable:", v.Type().AssignableTo(sr))
	p, ok := reflect.TypeAssert[fmt.Stringer](v)
	fmt.Println("typeassert:", ok, p == nil)

	fmt.Println("duration stringer:", reflect.TypeOf(time.Duration(0)).Implements(sr))
	fmt.Println("int stringer:", reflect.TypeOf(0).Implements(sr))
}
