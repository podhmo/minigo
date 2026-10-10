package main

import (
	"errors"
	"fmt"
)

type MyErr struct{ msg string }

func (e *MyErr) Error() string { return e.msg }

type Other struct{}

func (e *Other) Error() string { return "other" }

type VErr struct{ n int }

func (e VErr) Error() string { return fmt.Sprintf("v%d", e.n) }

func main() {
	var err error = fmt.Errorf("wrap: %w", &MyErr{msg: "boom"})
	e, ok := errors.AsType[*MyErr](err)
	fmt.Println(ok, e)
	e2, ok2 := errors.AsType[*Other](err)
	fmt.Println(ok2, e2)
	e3, ok3 := errors.AsType[error](err)
	fmt.Println(ok3, e3)

	var verr error = VErr{n: 3}
	v, okv := errors.AsType[VErr](verr)
	fmt.Println(okv, v)
	vp, okvp := errors.AsType[*VErr](verr)
	fmt.Println(okvp, vp)

	var nilErr error
	vn, okvn := errors.AsType[VErr](nilErr)
	fmt.Println(okvn, vn)
}
