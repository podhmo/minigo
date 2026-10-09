package main

import (
	"errors"
	"fmt"
)

// A host error value teaches T=error: the call sites that drop the
// argument's declared static type (a direct call result, a range var
// over []any, a `:=` var) still infer like gc instead of trapping on
// the unbound parameter — errors.New's result is statically `error`,
// and an `any` element has no better bind to offer.

func kind[T any](x T) string { return fmt.Sprintf("%T", x) }

func ident[T any](x T) T { return x }

func use[T interface{ Error() string }](x T) string { return x.Error() }

func pick[T any, U any](a T, b U) string { return fmt.Sprintf("%T|%T", a, b) }

func main() {
	fmt.Println(kind(errors.New("e")))
	var err error = errors.New("e")
	fmt.Println(kind(err))
	var v any = errors.New("e")
	fmt.Println(kind(v))
	x := errors.New("e")
	fmt.Println(kind(x))
	for _, el := range []any{1, errors.New("e2"), "s"} {
		fmt.Println(kind(el))
	}
	fmt.Println(use(errors.New("boom")))
	fmt.Println(ident(errors.New("x")).Error())
	fmt.Println(pick(1, errors.New("e")))
}
