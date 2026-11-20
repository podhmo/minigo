package main

import (
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
}
