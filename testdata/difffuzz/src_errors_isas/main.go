package main

import (
	"context"
	"errors"
	"fmt"
)

type E struct{ msg string }

func (e *E) Error() string { return "E:" + e.msg }

type Wrapped struct{ inner error }

func (w *Wrapped) Error() string { return "w" }
func (w *Wrapped) Unwrap() error { return w.inner }

func main() {
	e := &E{"x"}
	err := error(e)
	var t *E
	fmt.Println(errors.As(err, &t), t.msg)

	w := &Wrapped{inner: err}
	var t2 *E
	fmt.Println(errors.As(w, &t2), t2.msg)
	fmt.Println(errors.Is(w, err))

	var nilE *E
	errNil := error(nilE)
	fmt.Println(errNil != nil)
	var t3 *E
	fmt.Println(errors.As(errNil, &t3))

	asIntoErrorSlot()
}

func asIntoErrorSlot() {
	// As stores through reflect.Value.Set — the target error slot must
	// keep the typed nil's tag through a host error parameter.
	ctx, cancel := context.WithCancelCause(context.Background())
	var nilE *E
	var err error = nilE
	var target error
	if !errors.As(err, &target) {
		fmt.Println("as failed")
		return
	}
	cancel(target)
	c := context.Cause(ctx)
	fmt.Println(c != nil, errors.Is(c, context.Canceled))
}
