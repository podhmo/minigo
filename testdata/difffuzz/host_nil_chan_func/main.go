package main

// A nil chan or func a host API hands back (context.Background().Done(),
// exec.Cmd's unset Cancel) is a typed nil: it equals nil, but an
// interface holding it is not nil and keeps its dynamic type for %T,
// assertions, type switches and reflect. It still binds to typed
// slots and fields, and calling the nil func panics.

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
)

type S struct {
	D <-chan struct{}
	F func() error
}

func use(d <-chan struct{}, f func() error) bool { return d == nil && f == nil }

func main() {
	ctx := context.Background()
	d := ctx.Done()
	var x any = d
	fmt.Println(d == nil, x == nil)
	fmt.Printf("%T %v\n", x, x)
	_, ok := x.(<-chan struct{})
	fmt.Println(ok)
	select {
	case <-d:
		fmt.Println("done")
	default:
		fmt.Println("not done")
	}
	fmt.Println(reflect.DeepEqual(x, nil), reflect.TypeOf(x))

	var d2 <-chan struct{} = ctx.Done()
	fmt.Println(d2 == nil, any(d2) == any(d), any(ctx.Done()) == any((<-chan struct{})(nil)))
	switch v := any(ctx.Done()).(type) {
	case nil:
		fmt.Println("nil")
	case <-chan struct{}:
		fmt.Println("chan", v == nil)
	}

	cmd := exec.Command("true")
	var c any = cmd.Cancel
	fmt.Println(cmd.Cancel == nil, c == nil)
	fmt.Printf("%T\n", c)
	fn, ok := c.(func() error)
	fmt.Println(fn == nil, ok)

	s := S{D: ctx.Done(), F: cmd.Cancel}
	fmt.Println(s.D == nil, s.F == nil, use(ctx.Done(), cmd.Cancel))
	m := map[string]any{"d": ctx.Done(), "f": cmd.Cancel}
	fmt.Println(m["d"] == nil, m["f"] == nil)

	cmd.Cancel = func() error { return nil }
	fmt.Println(cmd.Cancel == nil, cmd.Cancel())

	var f func() error = exec.Command("true").Cancel
	defer func() { fmt.Println("recovered", recover() != nil) }()
	f()
}
